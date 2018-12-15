package main

import (
	"context"
	"fmt"
	"net"
	"time"

	cf "github.com/cloudflare/cloudflare-go"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

type resolver struct {
	addr     string
	resolver string
	ip       []net.IPAddr
}

func (dns *resolver) lookup(ctx context.Context) error {
	r := net.Resolver{
		PreferGo: true, // override system DNS
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "udp", fmt.Sprintf("%s:53", dns.resolver))
		},
	}

	ip, err := r.LookupIPAddr(ctx, dns.addr)
	if err != nil {
		return fmt.Errorf("DNS lookup error: %s", err)
	}

	dns.ip = ip
	return nil
}

func newPublicIP(ctx context.Context) (net.IP, error) {
	dns := resolver{
		addr:     "myip.opendns.com",
		resolver: "resolver1.opendns.com",
	}

	err := dns.lookup(ctx)
	if err != nil {
		return net.IP{}, err
	}

	return dns.ip[0].IP, nil
}

type dynIP struct {
	api      *cf.API
	zoneName string
	record   cf.DNSRecord
	aRecord  string
	rIP      net.IP
	dIP      net.IP
}

func (d *dynIP) getRecord() error {

	// Fetch the zone ID
	zoneID, err := d.api.ZoneIDByName(d.zoneName)
	if err != nil {
		return err
	}

	// Get all A records
	a := cf.DNSRecord{Type: "A"}
	recs, err := d.api.DNSRecords(zoneID, a)
	if err != nil {
		return err
	}

	// Get the contents of the matching A record
	for _, r := range recs {
		if r.Name == fmt.Sprintf("%s.%s", d.aRecord, d.zoneName) {
			d.record = r
			d.rIP = net.ParseIP(r.Content).To4()
			break
		}
	}

	return nil
}

func (d *dynIP) Sync() error {
	// Check if the dynamic and remote IPv4 addresses are equal
	if net.IP.Equal(d.dIP, d.rIP) {
		return nil
	}
	log.Warnf("DNS A record (%s) is out of sync with Dynamic IP (%s)", d.rIP, d.dIP)

	// Update the dynamic IP in Cloudflare
	record := d.record
	record.Content = d.dIP.String()
	err := d.api.UpdateDNSRecord(d.record.ZoneID, d.record.ID, record)
	if err != nil {
		return err
	}

	log.Infof("DNS A record (%s) has been synched with Dynamic IP (%s)", d.rIP, d.dIP)

	return nil
}

func main() {

	// Allow all configuration properties to be passed
	// as environment variables
	viper.AutomaticEnv()
	viper.SetEnvPrefix("DYN")

	// Set Viper configuration defaults
	viper.SetDefault("record", "@")

	// Load configuration
	viper.SetConfigName("config") // name of config file without extension
	viper.AddConfigPath("/etc/dyn/")
	viper.AddConfigPath("$HOME/.dyn/")
	viper.AddConfigPath(".")

	err := viper.ReadInConfig()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}

	log.Infof("configuration: loading configuration file from '%s'", viper.ConfigFileUsed())

	// Construct a new API object
	api, err := cf.New(viper.GetString("cloudflare.apiKey"), viper.GetString("cloudflare.email"))
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	tick, err := time.ParseDuration(viper.GetString("tick"))
	if err != nil {
		log.Fatal(err)
	}

	for range time.NewTicker(tick).C {
		// Get the current dynamic IP
		dIP, err := newPublicIP(ctx)
		if err != nil {
			log.Error(err)
			return
		}

		dyn := dynIP{
			api:      api,
			zoneName: viper.GetString("dns.zone"),
			aRecord:  viper.GetString("dns.record"),
			dIP:      dIP,
		}

		err = dyn.getRecord()
		if err != nil {
			log.Printf("error getting remote ip: %s", err)
		}

		err = dyn.Sync()
		if err != nil {
			log.Printf("error syncing remote DNS: %s", err)
		}
	}
}

func init() {
	// Display full timestamps in all logs by default
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})
}
