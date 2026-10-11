package main

import (
	"reflect"
	"testing"
)

func TestStandaloneSOCKSAccessConfiguration(t *testing.T) {
	config, err := parseServerArguments([]string{"--data-dir=/tmp/owned-server", "--socks-listen=0.0.0.0:0", "--socks-access-address=Proxy.Example:01080"})
	if err != nil {
		t.Fatal("advertised endpoint rejected", err)
	}
	field := reflect.ValueOf(config).FieldByName("socksAccessAddress")
	if !field.IsValid() || field.String() != "proxy.example:1080" {
		t.Fatal("canonical advertised endpoint missing")
	}
	for _, flags := range [][]string{{"--socks-access-address=proxy.example:1080"}, {"--socks-listen=127.0.0.1:0", "--socks-access-address=0.0.0.0:1080"}, {"--socks-listen=127.0.0.1:0", "--socks-access-address=proxy.example:0"}, {"--socks-listen=127.0.0.1:0", "--socks-access-address=user@proxy.example:1080"}, {"--socks-listen=127.0.0.1:0", "--socks-access-address=proxy.example:1080/path"}} {
		if _, err := parseServerArguments(append([]string{"--data-dir=/tmp/owned-server"}, flags...)); err == nil {
			t.Fatalf("invalid advertised SOCKS flags accepted: %v", flags)
		}
	}
	desktopArgs := []string{"--app-cache-dir=/tmp/cache", "--data-dir=/tmp/data", "--webview-origin=vibermate://desktop", "--bootstrap-fd=1", "--parent-lifetime-fd=0", "--socks-access-address=proxy.example:1080"}
	if _, err := parseCommandConfig(desktopArgs); err == nil {
		t.Fatal("Desktop accepted public advertised SOCKS flag")
	}
}

func TestExplicitSOCKSAndTrustedVLESSStartupConfiguration(t *testing.T) {
	server, e := parseServerArguments([]string{"--data-dir=/tmp/owned-server", "--socks-listen=127.0.0.1:0", "--vless-executable=/tmp/owned-xray"})
	if e != nil {
		t.Fatal("explicit Server packet startup rejected", e)
	}
	for name, want := range map[string]string{"socksListenAddress": "127.0.0.1:0", "vlessExecutable": "/tmp/owned-xray"} {
		if got := reflect.ValueOf(server).FieldByName(name); !got.IsValid() || got.String() != want {
			t.Fatal("Server startup setting missing", name)
		}
	}
	desktopArgs := []string{"--app-cache-dir=/tmp/cache", "--data-dir=/tmp/data", "--webview-origin=vibermate://desktop", "--bootstrap-fd=1", "--parent-lifetime-fd=0"}
	desktop, e := parseCommandConfig(append(desktopArgs, "--socks-listen=127.0.0.1:0", "--vless-executable=/tmp/owned-xray"))
	if e != nil {
		t.Fatal("explicit Desktop packet startup rejected", e)
	}
	for name, want := range map[string]string{"socksListenAddress": "127.0.0.1:0", "vlessExecutable": "/tmp/owned-xray"} {
		if got := reflect.ValueOf(desktop).FieldByName(name); !got.IsValid() || got.String() != want {
			t.Fatal("Desktop startup setting missing", name)
		}
	}
	for _, flags := range [][]string{{"--socks-listen=localhost:0"}, {"--vless-executable=relative-xray"}, {"--socks-listen=127.0.0.1:0", "--socks-listen=127.0.0.1:1"}} {
		if _, e := parseServerArguments(append([]string{"--data-dir=/tmp/server"}, flags...)); e == nil {
			t.Fatal("unsafe/ambiguous Server startup accepted", flags)
		}
	}
	if _, e := parseCommandConfig(append(desktopArgs, "--socks-listen=0.0.0.0:0")); e == nil {
		t.Fatal("Desktop public packet proxy accepted")
	}
}
