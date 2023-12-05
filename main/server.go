package main

import (
	"context"
	"fmt"
	"github.com/spf13/viper"
	"io/ioutil"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Cloudflare CloudflareConfig `mapstructure:"cloudflare"`
}

type CloudflareConfig struct {
	Key        string `mapstructure:"key"`
	User       string `mapstructure:"user"`
	RecordName string `mapstructure:"record_name"`
	RecordType string `mapstructure:"record_type"`
	TTL        int    `mapstructure:"ttl"`
	ZoneID     string `mapstructure:"zone_id"`
	RecordID   string `mapstructure:"record_id"`
	TestDomain string `mapstructure:"test_domain"`
}

func updateDDNS(fastestIP string, cfConfig CloudflareConfig) {
	client := &http.Client{}
	fmt.Println("首次设置可访问IP:", fastestIP)
	jsonStr := fmt.Sprintf(`{"id":"%s","type":"%s","name":"%s","content":"%s", "ttl":%d}`, cfConfig.ZoneID, cfConfig.RecordType, cfConfig.RecordName, fastestIP, cfConfig.TTL)
	req, err := http.NewRequest("PUT", fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", cfConfig.ZoneID, cfConfig.RecordID), strings.NewReader(jsonStr))
	if err != nil {
		fmt.Println("Failed to create Cloudflare request:", err)
		return
	}
	req.Header.Set("X-Auth-Email", cfConfig.User)
	req.Header.Set("X-Auth-Key", cfConfig.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Failed to update DNS record:", err)
		return
	}
	defer resp.Body.Close()
	responseBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Failed to read Cloudflare response:", err)
		return
	}
	fmt.Println(string(responseBody))
}

func getIpSpeed(ip string, cfConfig CloudflareConfig) float64 {
	client := &http.Client{
		Timeout: 2 * time.Second,
	}
	start := time.Now()
	fmt.Println("request ip ==> :", ip)
	dialer := &net.Dialer{
		Timeout:   2 * time.Second,
		KeepAlive: 2 * time.Second,
	}
	http.DefaultTransport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == cfConfig.TestDomain+":2083" {
			addr = fmt.Sprintf("%s:443", ip)
		}
		return dialer.DialContext(ctx, network, addr)
	}
	req, err := http.NewRequest("GET", fmt.Sprintf("https://%s:2083/clientarea.php", cfConfig.TestDomain), nil)
	if err != nil {
		fmt.Println("Failed to create request:", err)
		return math.MaxFloat64
	}
	req.Header.Set("Connection", "close")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Request failed for IP:", err)
		return math.MaxFloat64
	}
	fmt.Println("close  request body:", ip)
	defer resp.Body.Close()
	var speed = float64(10000.00 / time.Since(start))
	return speed
}
func main() {
	if len(os.Args) < 2 {
		fmt.Println("Please provide the URL as a parameter.")
		os.Exit(1)
	}
	viper.SetConfigFile("config.toml")
	err := viper.ReadInConfig()
	if err != nil {
		fmt.Println("Failed to read config file:", err)
		return
	}

	var config Config
	err = viper.Unmarshal(&config)
	if err != nil {
		fmt.Println("Failed to unmarshal config file:", err)
		return
	}

	url := os.Args[1]
	resp, err := http.Get(url)
	if err != nil {
		fmt.Println("Failed to fetch IP list:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	ipList, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Failed to read IP list:", err)
		os.Exit(1)
	}
	ipArray := strings.Split(string(ipList), "\n")
	fastestIP := ""
	fastestSpeed := 999999.0
	var hasSetIp = false
	var wg sync.WaitGroup
	var mutex sync.Mutex
	maxConcurrency := 10 // 设置最大并发协程数量
	semaphore := make(chan struct{}, maxConcurrency)

	for _, ip := range ipArray {
		if ip == "" {
			continue
		}
		semaphore <- struct{}{} // 协程数量超过最大限制时，阻塞在此，等待其他协程结束释放信号量
		wg.Add(1)
		go func(ip string) {
			defer func() {
				<-semaphore // 协程结束后释放信号量，允许其他协程启动
				wg.Done()
			}()
			var speed = getIpSpeed(ip, config.Cloudflare)
			mutex.Lock()
			if resp.StatusCode == http.StatusBadRequest {
				fmt.Printf("%s response -> %f\n", ip, speed)
				if speed < fastestSpeed {
					fastestIP = ip
					fastestSpeed = speed
				}
				if !hasSetIp && resp.StatusCode == http.StatusBadRequest {
					updateDDNS(ip, config.Cloudflare)
					hasSetIp = true
				}
			}
			mutex.Unlock()
		}(ip)
	}
	wg.Wait()
	if fastestIP == "" {
		fmt.Println("没有可用IP")
	} else {
		fmt.Println("最快的IP是:", fastestIP)
		updateDDNS(fastestIP, config.Cloudflare)
	}
}
