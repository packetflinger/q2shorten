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
	"time"

	pb "github.com/packetflinger/q2shorten/proto"
	"google.golang.org/protobuf/encoding/prototext"
)

var (
	configfile = flag.String("config", "short.config", "config file for http server")
	logfile    = flag.String("logfile", "short.log", "The file we should log to")
	foreground = flag.Bool("foreground", false, "Log to STDOUT/STDERR instead of file")
	configpb   pb.Config
	serviceMap map[string]*pb.Mapping
)

func main() {
	flag.Parse()

	logfile, err := os.OpenFile(*logfile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening log file: %v", err)
	}
	defer logfile.Close()
	if !*foreground {
		log.SetOutput(logfile)
	}

	contents, err := os.ReadFile(*configfile)
	if err != nil {
		log.Fatalln(err)
	}
	err = prototext.Unmarshal(contents, &configpb)
	if err != nil {
		log.Fatalln(err)
	}

	contents, err = os.ReadFile(configpb.GetMapFile())
	if err != nil {
		log.Fatalln(err)
	}
	var mappingpb pb.Mappings
	err = prototext.Unmarshal(contents, &mappingpb)
	if err != nil {
		log.Fatalln(err)
	}

	serviceMap = make(map[string]*pb.Mapping)
	for _, m := range mappingpb.GetMapping() {
		for _, l := range m.GetName() {
			serviceMap[l] = m
		}
	}

	address := fmt.Sprintf("%s:%d", configpb.GetAddress(), configpb.GetPort())
	httpsrv := &http.Server{
		Addr:         address,
		WriteTimeout: 10 * time.Second,
		ReadTimeout:  10 * time.Second,
	}

	// all requests will be handled by this func
	http.HandleFunc("/", redirectHandler)

	log.Println("loaded", len(serviceMap), "service mappings from", configpb.GetMapFile())
	log.Printf("listening on http://%s\n", address)
	log.Fatal(httpsrv.ListenAndServe())
}

// whether this mapping should be allowed based on times
func allowed(mapping *pb.Mapping) bool {
	now := time.Now().Unix()
	if mapping == nil {
		return false
	}
	if now < mapping.GetPremierTime() {
		return false
	}
	if mapping.GetExpireTime() > 0 && now > mapping.GetExpireTime() {
		return false
	}
	return true
}

func redirectHandler(w http.ResponseWriter, r *http.Request) {
	mapping, found := serviceMap[r.URL.Path[1:]]

	if found && allowed(mapping) {
		code := http.StatusSeeOther // 303
		if mapping.GetHttpCode() > 0 {
			code = int(mapping.GetHttpCode())
		}
		log.Println(r.RemoteAddr, r.URL.Path, "->", mapping.GetTarget())
		http.Redirect(w, r, mapping.GetTarget(), code)
	} else {
		log.Println(r.RemoteAddr, r.URL.Path, "-> ???")
		fmt.Fprintln(w, "unknown service")
	}
}
