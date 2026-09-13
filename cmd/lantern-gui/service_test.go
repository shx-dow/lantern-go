package main

import (
	"testing"
)

func TestGuiServiceProxiesDaemon(t *testing.T) {
	srv := fakeDaemon(t, "tok")
	defer srv.Close()

	svc := NewGuiService(srv.URL, "tok")
	if info := svc.AppInfo(); info.Name != "Lantern" || info.Version == "" {
		t.Fatalf("bad AppInfo: %+v", info)
	}
	rec, err := svc.ShareFile("/tmp/photo.jpg")
	if err != nil || rec.Code != "abc" {
		t.Fatalf("ShareFile: %+v %v", rec, err)
	}
	fetched, err := svc.FetchCode("abc", ".")
	if err != nil || fetched.Kind != "fetch" {
		t.Fatalf("FetchCode: %+v %v", fetched, err)
	}
	one, err := svc.GetTransfer("abc")
	if err != nil || one.FileName != "photo.jpg" {
		t.Fatalf("GetTransfer: %+v %v", one, err)
	}
	if err := svc.CancelTransfer("abc"); err != nil {
		t.Fatalf("CancelTransfer: %v", err)
	}
	hist, err := svc.ListHistory()
	if err != nil || hist == nil {
		t.Fatalf("ListHistory: %+v %v", hist, err)
	}
	st, err := svc.GetStatus()
	if err != nil || st.PeerID != "peer123" {
		t.Fatalf("GetStatus: %+v %v", st, err)
	}
	peers, err := svc.ListPeers()
	if err != nil || len(peers) != 1 {
		t.Fatalf("ListPeers: %+v %v", peers, err)
	}
	up, err := svc.UploadData("photo.jpg", "MDEyMzQ1Njc4OQ==")
	if err != nil || up.Path == "" {
		t.Fatalf("UploadData: %+v %v", up, err)
	}
	if _, err := svc.UploadData("photo.jpg", "!!!not-base64!!!"); err == nil {
		t.Fatal("expected base64 error, got nil")
	}
}

func TestGuiServicePropagatesErrors(t *testing.T) {
	srv := fakeDaemon(t, "tok")
	defer srv.Close()

	svc := NewGuiService(srv.URL, "bad-token")
	if _, err := svc.GetStatus(); err == nil {
		t.Fatal("expected auth error, got nil")
	}
}
