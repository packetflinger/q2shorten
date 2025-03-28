// A VERY simple URL shortener intended for use with Quake 2 servers. Often
// URLs to configs or pak files need to be given to players via MOTD messages
// or via chat.
//
// This is intended not to be directly accessible to the public internet but
// rather reverse-proxied by something like nginx that would handle TLS.
//
// The config file is passed in with the --config flag (default is
// "short.config"). URL mappings are defined in a text-format proto file set
// in the config.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	pb "github.com/packetflinger/q2shorten/proto"
	"google.golang.org/protobuf/encoding/prototext"
)

var (
	configfile = flag.String("config", "short.config", "config file for http server")
	logfile    = flag.String("logfile", "short.log", "The file we should log to")
	foreground = flag.Bool("foreground", false, "Log to STDOUT/STDERR instead of file")
	validate   = flag.Bool("validate", false, "Validates the mappings")

	config     *pb.Config
	serviceMap map[string]*pb.Mapping
)

func main() {
	flag.Parse()
	if *validate {
		config, err := loadConfig(*configfile)
		if err != nil {
			log.Fatal(err)
		}

		_, err = loadMapping(config.GetMapFile())
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("ok")
		return
	}

	var err error
	if !*foreground {
		logfile, err := os.OpenFile(*logfile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("error opening log file: %v", err)
		}
		defer logfile.Close()
		log.SetOutput(logfile)
	}

	config, err = loadConfig(*configfile)
	if err != nil {
		log.Fatal(err)
	}

	serviceMap, err = loadMapping(config.GetMapFile())
	if err != nil {
		log.Fatal(err)
	}

	address := fmt.Sprintf("%s:%d", config.GetAddress(), config.GetPort())
	httpsrv := &http.Server{
		Addr:         address,
		WriteTimeout: 10 * time.Second,
		ReadTimeout:  10 * time.Second,
	}

	// all requests will be handled by this func
	http.HandleFunc("/", redirectHandler)

	log.Println("loaded", len(serviceMap), "service mappings from", config.GetMapFile())
	log.Printf("listening on http://%s\n", address)
	log.Fatal(httpsrv.ListenAndServe())
}

// Read the config file and return a binary proto representing the contents
func loadConfig(cfg string) (*pb.Config, error) {
	var configpb pb.Config
	contents, err := os.ReadFile(cfg)
	if err != nil {
		return nil, err
	}
	err = prototext.Unmarshal(contents, &configpb)
	if err != nil {
		return nil, err
	}
	return &configpb, nil
}

// Read the mapping file and generate a map of services
func loadMapping(mapfile string) (map[string]*pb.Mapping, error) {
	contents, err := os.ReadFile(mapfile)
	if err != nil {
		return nil, fmt.Errorf("loadMapping() read error: %v", err)
	}
	var mappingpb pb.Mappings
	err = prototext.Unmarshal(contents, &mappingpb)
	if err != nil {
		return nil, fmt.Errorf("loadMapping() unmarshal error: %v", err)
	}
	serviceMap = make(map[string]*pb.Mapping)
	for _, m := range mappingpb.GetMapping() {
		for _, l := range m.GetName() {
			serviceMap[l] = m
		}
	}
	return serviceMap, nil
}

// whether this mapping should be allowed at a particular time. The `when` arg
// is a unix timestamp
func allowed(mapping *pb.Mapping, when int64) bool {
	if mapping == nil {
		return false
	}
	if when < mapping.GetPremierTime() {
		return false
	}
	if mapping.GetExpireTime() > 0 && when > mapping.GetExpireTime() {
		return false
	}
	return true
}

func redirectHandler(w http.ResponseWriter, r *http.Request) {
	// try to use real IP via proxy, if not fall back to remoteaddr
	ip := r.Header.Get("X-Real-IP")
	if len(ip) == 0 {
		ip = r.RemoteAddr
	}

	// special cases
	if r.URL.Path == "/favicon.ico" {
		return
	}
	if r.URL.Path == "/" {
		indexHandler(w, r)
		return
	}
	if r.URL.Path == "/robots.txt" {
		robotsHandler(w, r)
		return
	}
	if r.URL.Path == "/list" || r.URL.Path == "/index" {
		listHandler(w, r)
		log.Println(ip, r.URL.Path)
		return
	}
	if r.URL.Path == "/rehash" {
		rehashHandler(w, r)
		return
	}

	svc, found := serviceMap[r.URL.Path[1:]]

	if found && allowed(svc, time.Now().Unix()) {
		code := http.StatusSeeOther // 303
		if svc.GetHttpCode() > 0 {
			code = int(svc.GetHttpCode())
		}
		log.Println(ip, r.URL.Path, "->", svc.GetTarget())
		http.Redirect(w, r, svc.GetTarget(), code)
	} else {
		log.Println(ip, r.URL.Path, "-> ???")
		fmt.Fprintln(w, "unknown service")
	}
}

// someone requested the domain with no service name. Instead
// of giving them some useless "unknown service" message, give
// them something slightly more useful.
func indexHandler(w http.ResponseWriter, _ *http.Request) {
	msg := "This is a simple URL shortener. To propose a new redirect go to q2.wtf/new"
	fmt.Fprintln(w, msg)
}

// special case: listout all mappings
func listHandler(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().Unix()

	// sort the keys
	keys := []string{}
	for k := range serviceMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprintln(w, "All mappings:")
	for _, k := range keys {
		v := serviceMap[k]
		if v.GetExpireTime() > 0 && now > v.GetExpireTime() {
			continue
		}
		if now < v.GetPremierTime() {
			continue
		}

		fmt.Fprintf(w, "%-20s %s\n", k, v.GetTarget())
	}
}

// Reload the mappings from disk
func rehashHandler(w http.ResponseWriter, r *http.Request) {
	var err error
	if config.GetRehashKey() == "" {
		fmt.Fprintln(w, "auth key not set")
		return
	}
	log.Println(r.RemoteAddr, "rehash requested")
	key := r.URL.Query().Get("key")
	if config.GetRehashKey() != key {
		fmt.Fprintln(w, "invalid auth key")
		return
	}
	backup := serviceMap
	serviceMap, err = loadMapping(config.GetMapFile())
	if err != nil {
		serviceMap = backup
		log.Printf("rehash error: %v", err)
		fmt.Fprintln(w, "error reloading service map")
		return
	}
	fmt.Fprintf(w, "Rehash: OK\nLoaded %d services", len(serviceMap))
}

// Handle requests for robots.txt. Hard coded, we don't want anything crawled.
func robotsHandler(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintf(w, "User-agent: *\nDisallow: /\n")
}
