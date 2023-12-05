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
	Key         string `mapstructure:"key"`
	User        string `mapstructure:"user"`
	RecordName  string `mapstructure:"record_name"`
	HttpTimeOut int    `mapstructure:"http_time_out"`
	RecordType  string `mapstructure:"record_type"`
	TTL         int    `mapstructure:"ttl"`
	ZoneID      string `mapstructure:"zone_id"`
	RecordID    string `mapstructure:"record_id"`
	TestDomain  string `mapstructure:"test_domain"`
	TestPort    string `mapstructure:"test_port"`
	UpdateUrl   string `mapstructure:"update_url"`
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
	start := time.Now()
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Resolver: &net.Resolver{},
		}).DialContext,
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip, cfConfig.TestPort))
	}
	// 创建一个自定义的 Client，使用自定义的 Transport
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(cfConfig.HttpTimeOut) * time.Second,
	}
	req, err := http.NewRequest("GET", fmt.Sprintf("https://%s:%s/clientarea.php", cfConfig.TestDomain, cfConfig.TestPort), nil)
	if err != nil {
		fmt.Println("Failed to create request:", err, "ip->", ip)
		return math.MaxFloat64
	}
	req.Header.Set("Connection", "close")
	resp, err := client.Do(req)

	if resp != nil {
		defer resp.Body.Close()
	}
	if resp != nil && resp.StatusCode == 400 {
		speed := 100.00 / time.Since(start).Seconds()
		fmt.Println("valid  ip :", ip, "speed=>", speed)
		return speed
	} else {
		fmt.Println("broken ip -> ", ip)
	}
	return math.MaxFloat64
}
func main() {
	if len(os.Args) < 2 {
		fmt.Println("Please input config")
		os.Exit(1)
	}
	viper.SetConfigFile(os.Args[1])
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
	resp, err := http.Get(config.Cloudflare.UpdateUrl)
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
