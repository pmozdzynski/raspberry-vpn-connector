package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"vpn-connector/handlers"
)

func checkRootPrivileges() {
	if os.Geteuid() != 0 {
		log.Fatal("This program must be run as root. Try: sudo ./vpn-connector")
	}
}

func main() {
	checkRootPrivileges()

	http.HandleFunc("/setup", handlers.SetupPageHandler)
	http.HandleFunc("/setup/status", handlers.SetupStatusHandler)
	http.HandleFunc("/setup/apply", handlers.SetupApplyHandler)

	http.HandleFunc("/login", handlers.LoginHandler)
	http.HandleFunc("/logout", handlers.LogoutHandler)

	fs := http.FileServer(http.Dir("./templates"))
	serveStatic := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			fs.ServeHTTP(w, r)
		}
	}
	http.HandleFunc("/styles.css", serveStatic("styles.css"))
	http.HandleFunc("/app.js", serveStatic("app.js"))
	http.HandleFunc("/setup.js", serveStatic("setup.js"))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !handlers.IsConfigured() {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		handlers.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.ServeFile(w, r, "./templates/index.html")
				return
			}
			fs.ServeHTTP(w, r)
		})(w, r)
	})

	http.HandleFunc("/status", handlers.RequireAuth(handlers.StatusHandler))
	http.HandleFunc("/api/profiles", handlers.RequireAuth(handlers.ProfilesHandler))
	http.HandleFunc("/api/vpn/connect", handlers.RequireAuth(handlers.VPNConnectHandler))
	http.HandleFunc("/api/vpn/input", handlers.RequireAuth(handlers.VPNInputHandler))
	http.HandleFunc("/api/vpn/disconnect", handlers.RequireAuth(handlers.VPNDisconnectHandler))
	http.HandleFunc("/api/vpn/reconnect", handlers.RequireAuth(handlers.VPNReconnectHandler))

	go func() {
		log.Println("Starting server on :5000")
		if handlers.IsConfigured() {
			log.Println("Dashboard at http://<device-ip>:5000/")
		} else {
			log.Println("First boot. Open http://<device-ip>:5000/setup")
		}
		if err := http.ListenAndServe(":5000", nil); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	time.Sleep(2 * time.Second)

	if err := handlers.EnsureIPForwarding(); err != nil {
		log.Printf("Warning: IP forwarding: %v", err)
	}

	if handlers.IsConfigured() {
		go restoreVPNState()
	}

	select {}
}

func restoreVPNState() {
	st := handlers.GetVPNState()
	if st.Connected && st.TunIface != "" {
		log.Printf("VPN tunnel %s detected after reboot; applying VPN NAT", st.TunIface)
		_ = handlers.ApplyVPNNAT(st.TunIface)
		return
	}
	_ = handlers.ApplyDirectNAT()
	log.Println("Connect from the dashboard; FortiToken may be required after password")
}
