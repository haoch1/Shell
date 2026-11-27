package local

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

func encodeUTF16LEBOM(s string) []byte {
	u := utf16.Encode([]rune(s))
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xFE})
	for _, v := range u {
		_ = binary.Write(&buf, binary.LittleEndian, v)
	}
	return buf.Bytes()
}

func TestUniversalSSHImporters(t *testing.T) {
	isolated(t)

	// 1. Xshell .xsh (UTF-16LE BOM) inside .zip archive with nested folders
	zipPath := filepath.Join(t.TempDir(), "xshell-export.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("Sessions/Prod/Tokyo/web-01.xsh")
	xsh := "[SessionInfo]\r\nVersion=7.0\r\n[CONNECTION]\r\nProtocol=SSH\r\nHost=10.1.2.3\r\nPort=2222\r\n[CONNECTION:AUTHENTICATION]\r\nUserName=deploy\r\nMethod=Password\r\n"
	_, _ = w.Write(encodeUTF16LEBOM(xsh))
	zw.Close()
	zf.Close()

	res, err := ParseImportFiles([]string{zipPath})
	if err != nil || len(res.Hosts) != 1 {
		t.Fatalf("Xshell zip failed: %+v, %v", res, err)
	}
	if res.Hosts[0].Host != "10.1.2.3" || res.Hosts[0].Port != 2222 || res.Hosts[0].User != "deploy" || res.Hosts[0].Group != "Prod/Tokyo" {
		t.Fatalf("Xshell parsed mismatch: %+v", res.Hosts[0])
	}

	// 2. MobaXterm .mxtsessions
	moba := "[Bookmarks]\r\nSubRep=\r\nImgNum=42\r\n[Bookmarks_1]\r\nSubRep=Cloud\\AWS\r\nImgNum=41\r\nAPI-Server=#109#0%203.0.113.5%2200%ubuntu%%-1%-1%%%%%0%0%0%%%-1%0%0%0%%1080%%0%0%1#MobaFont\r\n"
	res, err = ParseImportBytes("sessions.mxtsessions", []byte(moba))
	if err != nil || len(res.Hosts) != 1 || res.Hosts[0].Group != "Cloud/AWS" || res.Hosts[0].Port != 2200 || res.Hosts[0].User != "ubuntu" {
		t.Fatalf("MobaXterm failed: %+v, %v", res, err)
	}

	// 3. PuTTY .reg
	putty := "Windows Registry Editor Version 5.00\r\n\r\n[HKEY_CURRENT_USER\\Software\\SimonTatham\\PuTTY\\Sessions\\Prod%20DB]\r\n\"HostName\"=\"db.internal.dev\"\r\n\"PortNumber\"=dword:000008ae\r\n\"UserName\"=\"pgadmin\"\r\n\"Protocol\"=\"ssh\"\r\n"
	res, err = ParseImportBytes("putty.reg", []byte(putty))
	if err != nil || len(res.Hosts) != 1 || res.Hosts[0].Name != "Prod DB" || res.Hosts[0].Port != 2222 {
		t.Fatalf("PuTTY .reg failed: %+v, %v", res, err)
	}

	// 4. Electerm bookmarks JSON
	electerm := `{"bookmarkGroups":[{"id":"g1","title":"Staging","bookmarkIds":["b1"]}],"bookmarks":[{"id":"b1","title":"Stage Web","host":"10.0.5.5","port":22,"username":"ops"}]}`
	res, err = ParseImportBytes("electerm.json", []byte(electerm))
	if err != nil || len(res.Hosts) != 1 || res.Hosts[0].Group != "Staging" {
		t.Fatalf("Electerm failed: %+v, %v", res, err)
	}

	// 5. WindTerm user.sessions
	windterm := `[{"session.label":"Edge","session.group":"/CN/Edge","session.protocol":"SSH","session.target":"root@192.168.10.2","session.port":22}]`
	res, err = ParseImportBytes("user.sessions", []byte(windterm))
	if err != nil || len(res.Hosts) != 1 || res.Hosts[0].Group != "CN/Edge" || res.Hosts[0].Host != "192.168.10.2" {
		t.Fatalf("WindTerm failed: %+v, %v", res, err)
	}

	// 6. Tabby YAML
	tabby := "profiles:\n  - type: ssh\n    name: K8s Master\n    group: Cluster\n    options:\n      host: 10.9.0.1\n      port: 2222\n      user: k8s\n"
	res, err = ParseImportBytes("config.yaml", []byte(tabby))
	if err != nil || len(res.Hosts) != 1 || res.Hosts[0].Host != "10.9.0.1" || res.Hosts[0].Group != "Cluster" {
		t.Fatalf("Tabby YAML failed: %+v, %v", res, err)
	}

	// 7. SecureCRT XML
	scrt := `<?xml version="1.0" encoding="UTF-8"?><VanDyke><key name="Sessions"><key name="HK"><key name="Gateway"><string name="Hostname">hk.example.com</string><string name="Username">admin</string><dword name="[SSH2] Port">2202</dword><string name="Protocol Name">SSH2</string></key></key></key></VanDyke>`
	res, err = ParseImportBytes("Sessions.xml", []byte(scrt))
	if err != nil || len(res.Hosts) != 1 || res.Hosts[0].Group != "HK" || res.Hosts[0].Port != 2202 {
		t.Fatalf("SecureCRT XML failed: %+v, %v", res, err)
	}

	// 8. Termius CSV + Import preserving original group when overrideGroup == ""
	csvData := "Label,Hostname,Port,Username,Groups\nGPU Node,192.168.1.99,22,ai,Lab\n"
	res, err = ParseImportBytes("termius.csv", []byte(csvData))
	if err != nil || len(res.Hosts) != 1 || res.Hosts[0].Group != "Lab" {
		t.Fatalf("Termius CSV failed: %+v, %v", res, err)
	}
	saved, err := ImportParsedHosts(res.Hosts, "")
	if err != nil || len(saved) != 1 || saved[0].Group != "Lab" {
		t.Fatalf("ImportParsedHosts did not preserve original group: %+v, %v", saved, err)
	}

	// 9. Netcatty CSV (with Hostname/IP, Groups, Tags, Password, Proxy, ProxyJump + header-only empty file)
	netcattyHeader := "\xef\xbb\xbfGroups,Label,Tags,Notes,Hostname/IP,Protocol,Port,Username,Password,KeyPath,Passphrase,Proxy,ProxyJump,LocalShell,SshInputCompatibility\r\n"
	resEmpty, err := ParseImportBytes("hosts_export_2026-11-27.csv", []byte(netcattyHeader))
	if err != nil || resEmpty.DetectedFormat != "Netcatty (.csv)" || len(resEmpty.Warnings) == 0 {
		t.Fatalf("Netcatty header-only CSV should report Netcatty format with clear warning: %+v, %v", resEmpty, err)
	}
	netcattyFull := netcattyHeader + "Cloud/SG,SG - AWS,\"prod;aws\",Tokyo Edge,56.10.113.238,ssh,58222,root,s3cr3tPass,,,socks5://127.0.0.1:7890,,,\r\n"
	resNet, err := ParseImportBytes("hosts_export_2026-11-27.csv", []byte(netcattyFull))
	if err != nil || resNet.DetectedFormat != "Netcatty (.csv)" || len(resNet.Hosts) != 1 {
		t.Fatalf("Netcatty full CSV failed: %+v, %v", resNet, err)
	}
	h := resNet.Hosts[0]
	if h.Name != "SG - AWS" || h.Host != "56.10.113.238" || h.Port != 58222 || h.Group != "Cloud/SG" || h.Password != "s3cr3tPass" || h.ProxyType != "socks5" || h.ProxyPort != 7890 || len(h.Tags) != 2 {
		t.Fatalf("Netcatty parsed fields mismatch: %+v", h)
	}
	savedNet, err := ImportParsedHosts(resNet.Hosts, "")
	if err != nil || len(savedNet) != 1 {
		t.Fatalf("Netcatty commit failed: %v", err)
	}
	if sec, err := LoadSecret(savedNet[0]); err != nil || sec != "s3cr3tPass" {
		t.Fatalf("Expected Netcatty password saved to DPAPI, got %q err=%v", sec, err)
	}

	// 10. Netcatty CSV with label-based ProxyJump + inline user@host:port ProxyJump + system/direct proxy
	netcattyJumpCSV := netcattyHeader +
		"Prod,Bastion-HK,,Jump,47.88.1.1,ssh,22,ops,jumpPass,,,system,,,\r\n" +
		"Prod,Internal-DB,,DB,10.0.1.50,ssh,22,root,dbPass,,,direct,Bastion-HK,,\r\n" +
		"Prod,Edge-Node,,Edge,10.0.2.9,ssh,22,root,edgePass,,,,admin@198.51.100.7:2222,,\r\n"
	resJump, err := ParseImportBytes("netcatty_jumps.csv", []byte(netcattyJumpCSV))
	if err != nil || len(resJump.Hosts) != 3 {
		t.Fatalf("Netcatty jump CSV parse failed: %+v, %v", resJump, err)
	}
	if resJump.Hosts[0].ProxyType != "system" || resJump.Hosts[1].ProxyType != "direct" || resJump.Hosts[1].ProxyJump != "Bastion-HK" {
		t.Fatalf("Netcatty proxy/jump fields mismatch: %+v", resJump.Hosts)
	}
	savedJump, err := ImportParsedHosts(resJump.Hosts, "")
	if err != nil || len(savedJump) < 4 {
		t.Fatalf("Expected 3 hosts + 1 auto-created inline jump host, got %d err=%v", len(savedJump), err)
	}
	var bastionID, dbJumpID, edgeJumpID string
	for _, sp := range savedJump {
		switch sp.Name {
		case "Bastion-HK":
			bastionID = sp.ID
		case "Internal-DB":
			dbJumpID = sp.JumpHostID
		case "Edge-Node":
			edgeJumpID = sp.JumpHostID
		}
	}
	if bastionID == "" || dbJumpID != bastionID {
		t.Fatalf("Expected Internal-DB to link JumpHostID=%q, got %q", bastionID, dbJumpID)
	}
	if edgeJumpID == "" {
		t.Fatalf("Expected Edge-Node to link auto-created inline jump host")
	}

	// 11. PuTTY .reg with SOCKS5 proxy
	puttyProxy := "Windows Registry Editor Version 5.00\r\n\r\n[HKEY_CURRENT_USER\\Software\\SimonTatham\\PuTTY\\Sessions\\ProxyBox]\r\n\"HostName\"=\"203.0.113.5\"\r\n\"PortNumber\"=dword:00000016\r\n\"Protocol\"=\"ssh\"\r\n\"ProxyMethod\"=dword:00000003\r\n\"ProxyHost\"=\"127.0.0.1\"\r\n\"ProxyPort\"=dword:00001ed2\r\n"
	resPutty, err := ParseImportBytes("putty_proxy.reg", []byte(puttyProxy))
	if err != nil || len(resPutty.Hosts) != 1 || resPutty.Hosts[0].ProxyType != "socks5" || resPutty.Hosts[0].ProxyPort != 7890 {
		t.Fatalf("PuTTY SOCKS5 proxy parse failed: %+v, %v", resPutty, err)
	}
}
