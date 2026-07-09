package main

import (
	"log"
	"net"
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
	http.HandleFunc("/api/status", handlers.RequireAuth(handlers.StatusHandler))
	http.HandleFunc("/api/profiles", handlers.RequireAuth(handlers.ProfilesHandler))
	http.HandleFunc("/api/vpn/connect", handlers.RequireAuth(handlers.VPNConnectHandler))
	http.HandleFunc("/api/vpn/input", handlers.RequireAuth(handlers.VPNInputHandler))
	http.HandleFunc("/api/vpn/disconnect", handlers.RequireAuth(handlers.VPNDisconnectHandler))
	http.HandleFunc("/api/vpn/reconnect", handlers.RequireAuth(handlers.VPNReconnectHandler))
	http.HandleFunc("/api/tailscale/exit-node", handlers.RequireAuth(handlers.TailscaleExitNodeHandler))

	go serveDashboard()

	time.Sleep(2 * time.Second)

	if err := handlers.EnsureIPForwarding(); err != nil {
		log.Printf("Warning: IP forwarding: %v", err)
	}

	if handlers.IsConfigured() {
		if err := handlers.EnsureRouterServices(); err != nil {
			log.Printf("Warning: router services: %v", err)
		}
		go restoreVPNState()
	}

	select {}
}

func serveDashboard() {
	certPath, keyPath, err := handlers.EnsureTLSCert()
	if err != nil {
		log.Printf("TLS setup failed (%v); falling back to HTTP on :5000", err)
		log.Println("Dashboard at http://<device-ip>:5000/")
		if err := http.ListenAndServe(":5000", nil); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
		return
	}

	if handlers.IsConfigured() {
		log.Println("Dashboard at https://<device-ip>:5000/ (self-signed cert; accept the browser warning)")
	} else {
		log.Println("First boot. Open https://<device-ip>:5000/setup (self-signed cert; accept the browser warning)")
	}

	// Redirect plain HTTP on :80 to HTTPS so bookmarks and typos still work.
	go func() {
		redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
				host = h
			}
			http.Redirect(w, r, "https://"+host+":5000"+r.RequestURI, http.StatusMovedPermanently)
		})
		if err := http.ListenAndServe(":80", redirect); err != nil {
			log.Printf("HTTP redirect listener not started: %v", err)
		}
	}()

	log.Println("Starting HTTPS server on :5000")
	if err := http.ListenAndServeTLS(":5000", certPath, keyPath, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func restoreVPNState() {
	st := handlers.GetVPNState()
	if st.Connected && st.TunIface != "" {
		log.Printf("VPN tunnel %s detected after reboot; applying VPN NAT", st.TunIface)
		_ = handlers.ApplyVPNNAT(st.TunIface)
		handlers.StartManagementWatchdog(st.ServerURL)
		return
	}
	_ = handlers.ApplyDirectNAT()
	log.Println("Connect from the dashboard; FortiToken may be required after password")
}
