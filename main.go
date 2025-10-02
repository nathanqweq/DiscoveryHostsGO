package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Config representa o formato do arquivo discovery.conf
type Config struct {
	ZabbixURL     string   `json:"zabbix_url"`
	ZabbixUser    string   `json:"zabbix_user"`
	ZabbixPass    string   `json:"zabbix_pass"`
	ZabbixGroupID string   `json:"zabbix_group_id"`
	ZabbixProxyID string   `json:"zabbix_proxy_id"`
	SNMPCommunity string   `json:"snmp_community"`
	PingTimeout   int      `json:"ping_timeout"`
	SNMPTimeout   int      `json:"snmp_timeout"`
	Workers       int      `json:"workers"`
	Ranges        []string `json:"ranges"`
}

var config Config

func loadConfig(path string) {
	log.Printf("[INFO] Carregando arquivo de configuração: %s", path)
	data, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatalf("[ERRO] Falha ao ler discovery.conf: %v", err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		log.Fatalf("[ERRO] Falha ao parsear discovery.conf: %v", err)
	}
	log.Printf("[INFO] Configuração carregada com sucesso: %+v", config)
}

// Função simples de ping usando o comando do sistema
func ping(ip string, timeout int) bool {
	log.Printf("[PING] Testando IP %s", ip)
	cmd := exec.Command("ping", "-c", "1", "-W", fmt.Sprintf("%d", timeout), ip)
	err := cmd.Run()
	if err == nil {
		log.Printf("[PING] IP %s respondeu", ip)
		return true
	}
	log.Printf("[PING] IP %s não respondeu", ip)
	return false
}

// Consulta SNMP para obter sysName
func getSNMPName(ip string) (string, error) {
	log.Printf("[SNMP] Conectando ao host %s", ip)
	g := &gosnmp.GoSNMP{
		Target:    ip,
		Port:      161,
		Community: config.SNMPCommunity,
		Version:   gosnmp.Version2c,
		Timeout:   time.Duration(config.SNMPTimeout) * time.Second,
		Retries:   1,
	}
	err := g.Connect()
	if err != nil {
		log.Printf("[ERRO] Falha ao conectar SNMP em %s: %v", ip, err)
		return "", err
	}
	defer g.Conn.Close()

	oid := "1.3.6.1.2.1.1.5.0"
	result, err := g.Get([]string{oid})
	if err != nil {
		log.Printf("[ERRO] Falha na consulta SNMP em %s: %v", ip, err)
		return "", err
	}
	for _, variable := range result.Variables {
		if variable.Type == gosnmp.OctetString {
			sysName := string(variable.Value.([]byte))
			log.Printf("[SNMP] Host %s respondeu sysName: %s", ip, sysName)
			return sysName, nil
		}
	}
	log.Printf("[ERRO] OID não retornou string em %s", ip)
	return "", fmt.Errorf("OID não retornou string")
}

// Criação de host no Zabbix (simulada)
func createZabbixHost(name, ip string) error {
	log.Printf("[ZABBIX] Criando/verificando host %s (%s) no grupo %s via proxy %s", name, ip, config.ZabbixGroupID, config.ZabbixProxyID)
	// Aqui entraria chamada real à API do Zabbix
	return nil
}

func worker(wg *sync.WaitGroup, jobs <-chan string) {
	defer wg.Done()
	for ip := range jobs {
		if ping(ip, config.PingTimeout) {
			sysName, err := getSNMPName(ip)
			if err != nil {
				log.Printf("[WARN] Ping OK mas falha SNMP em %s: %v", ip, err)
				continue
			}
			_ = createZabbixHost(sysName, ip)
		}
	}
}

// Expande CIDR em lista de IPs
func expandCIDR(cidr string) ([]string, error) {
	log.Printf("[INFO] Expandindo range CIDR: %s", cidr)
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	var ips []string
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); inc(ip) {
		ips = append(ips, ip.String())
	}
	// remove network/broadcast
	if len(ips) > 2 {
		return ips[1 : len(ips)-1], nil
	}
	return ips, nil
}

// incrementa IP
func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func main() {
	log.Println("[INFO] Iniciando discovery...")
	loadConfig("discovery.conf")

	jobs := make(chan string, config.Workers)
	var wg sync.WaitGroup

	for w := 0; w < config.Workers; w++ {
		wg.Add(1)
		go worker(&wg, jobs)
	}

	for _, r := range config.Ranges {
		r = strings.TrimSpace(r)
		if strings.Contains(r, "/") { // CIDR
			ips, err := expandCIDR(r)
			if err != nil {
				log.Printf("[ERRO] Erro expandindo CIDR %s: %v", r, err)
				continue
			}
			for _, ip := range ips {
				jobs <- ip
			}
		} else {
			log.Printf("[INFO] Adicionando IP único: %s", r)
			jobs <- r
		}
	}

	close(jobs)
	wg.Wait()
	log.Println("[INFO] Discovery finalizado!")
}
