package main

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

func TestNativeFixtureOwnedPipesCancelAndJoin(t *testing.T) {
	for _, mode := range []string{"cancel", "already_canceled", "normal_cleanup", "repeated_cleanup", "cancel_cleanup_race"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			if mode == "already_canceled" {
				cancel()
			}
			cleanup := ownNativeFixturePipes(ctx, inR, inW, outR, outW)
			copierDone := make(chan struct{})
			go func() { defer close(copierDone); _, _ = io.Copy(io.Discard, inR) }()
			frames, scannerFinished := nativeClientFrames(ctx, outR)
			scannerDone := make(chan struct{})
			go func() { defer close(scannerDone); <-scannerFinished }()
			defer func() {
				cancel()
				_ = inR.Close()
				_ = inW.Close()
				_ = outR.Close()
				_ = outW.Close()
				cleanup()
				<-copierDone
				<-scannerDone
			}()
			if mode != "already_canceled" {
				if _, err := io.WriteString(inW, "synthetic stdin"); err != nil {
					t.Fatal("owner closed live stdin prematurely")
				}
				if _, err := io.WriteString(outW, "{\"id\":7,\"result\":{}}\n"); err != nil {
					t.Fatal("owner closed live stdout prematurely")
				}
				select {
				case frame, ok := <-frames:
					if !ok || string(frame["id"]) != "7" {
						t.Fatal("live frame truncated")
					}
				case <-time.After(200 * time.Millisecond):
					t.Fatal("live scanner did not forward frame")
				}
			}
			switch mode {
			case "cancel":
				cancel()
			case "already_canceled":
			case "normal_cleanup":
				cleanup()
			case "repeated_cleanup":
				cleanup()
				cleanup()
			case "cancel_cleanup_race":
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); cancel() }()
				go func() { defer wg.Done(); cleanup() }()
				wg.Wait()
			}
			timer := time.NewTimer(200 * time.Millisecond)
			defer timer.Stop()
			copyJoined, scanJoined := false, false
			copyWait, scanWait := copierDone, scannerDone
			for !copyJoined || !scanJoined {
				select {
				case <-copyWait:
					copyJoined = true
					copyWait = nil
				case <-scanWait:
					scanJoined = true
					scanWait = nil
				case <-timer.C:
					t.Errorf("owned pipe cancel did not join copy=%t scanner=%t", copyJoined, scanJoined)
					return
				}
			}
		})
	}
}
