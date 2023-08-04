package main

import (
	"fmt"
	"net/http"
	"strings"

	"internal/ws"

	"github.com/gorilla/websocket"
	log "github.com/pion/ion-sfu/pkg/logger"
	"github.com/pion/ion-sfu/pkg/sfu"
	"github.com/sourcegraph/jsonrpc2"
	websocketjsonrpc2 "github.com/sourcegraph/jsonrpc2/websocket"
	"github.com/spf13/viper"
)

var (
	conf   = sfu.Config{}
	logger = log.New()
)

// formatRequest generates ascii representation of a request
func formatRequest(r *http.Request) string {
	// Create return string
	var request []string
	// Add the request string
	url := fmt.Sprintf("%v %v %v", r.Method, r.URL, r.Proto)
	request = append(request, url)
	// Add the host
	request = append(request, fmt.Sprintf("Host: %v", r.Host))
	// Loop through headers
	for name, headers := range r.Header {
		name = strings.ToLower(name)
		for _, h := range headers {
			request = append(request, fmt.Sprintf("%v: %v", name, h))
		}
	}

	// If this is a POST, add post data
	if r.Method == "POST" {
		r.ParseForm()
		request = append(request, "\n")
		request = append(request, r.Form.Encode())
	}
	// Return the request as a string
	return strings.Join(request, "\n")
}

func main() {

	logger.Info("Starting Ria RTC server...")

	// build + start sfu

	viper.SetConfigFile("./config.toml")
	viper.SetConfigType("toml")
	err := viper.ReadInConfig()
	if err != nil {
		logger.Error(err, "error reading config")
		panic(err)
	}

	err = viper.Unmarshal(&conf)
	if err != nil {
		logger.Error(err, "error unmarshalling config")
		panic(err)
	}

	// start websocket server

	sfu.Logger = logger
	s := sfu.NewSFU(conf)

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		// TODO: If this seems slow, change to 0s for default vals
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Print("websocket version: ", r.Header.Get("Sec-WebSocket-Version"))
		fmt.Print("Request log", formatRequest(r))

		// Upgrading the HTTP request to the WebSocket protocol. The server inspects the request and if all is good the server sends an HTTP response agreeing to upgrade the connection.
		// conn is a websocket connection object
		logger.Info("Upgrading conn...")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			panic(err)
		}
		defer conn.Close()

		p := ws.NewConnection(sfu.NewPeer(s), logger)
		// p = {PeerLocal (NewPeer creates a new PeerLocal for signaling with the given SFU)
		// , logger}
		defer p.Close()

		jc := jsonrpc2.NewConn(r.Context(), websocketjsonrpc2.NewObjectStream(conn), p)
		<-jc.DisconnectNotify()
	})

	// Start the server and listen on port 8080.
	// port := 36710
	fmt.Printf("Starting server at port 36710\n")
	// if err := http.ListenAndServe(":36710", nil); err != nil {
	//     fmt.Println(err)
	// }
	if err := http.ListenAndServeTLS(":36710", "/etc/letsencrypt/live/matherium.com/fullchain.pem", "/etc/letsencrypt/live/matherium.com/privkey.pem", nil); err != nil {
		fmt.Println(err)
	}
}
