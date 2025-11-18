package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	maxminddb "github.com/oschwald/maxminddb-golang"
)

const (
	apiURL   = "https://api.github.com/repos/P3TERX/GeoLite.mmdb/releases/latest"
	saveMMDB = "/usr/share/GeoIP/GeoLite2-Country.mmdb"
	tmpMMDB  = "/tmp/GeoLite2-Country.mmdb"

	outCN4 = "/etc/nftables.d/cn4.nft"
	outCN6 = "/etc/nftables.d/cn6.nft"
)

type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type GitHubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []GitHubAsset `json:"assets"`
}

type CountryRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

func logInfo(msg string) {
	fmt.Printf("[%s] INFO: %s\n", time.Now().Format(time.RFC3339), msg)
}

func logErr(err error) {
	fmt.Printf("[%s] ERROR: %v\n", time.Now().Format(time.RFC3339), err)
}

func main() {
	logInfo("Fetching latest GitHub release metadata...")

	// 1. Fetch GitHub release info
	resp, err := http.Get(apiURL)
	if err != nil {
		logErr(err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		logErr(err)
		os.Exit(1)
	}

	logInfo("Latest tag: " + release.TagName)

	// 2. Find mmdb download URL
	var downloadURL string
	for _, a := range release.Assets {
		if filepath.Ext(a.Name) == ".mmdb" {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		logErr(fmt.Errorf("no mmdb file found in release"))
		os.Exit(1)
	}

	logInfo("MMDB download URL: " + downloadURL)

	// 3. Download mmdb
	logInfo("Downloading MMDB...")

	out, err := os.Create(tmpMMDB)
	if err != nil {
		logErr(err)
		os.Exit(1)
	}
	defer out.Close()

	resp2, err := http.Get(downloadURL)
	if err != nil {
		logErr(err)
		os.Exit(1)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != 200 {
		logErr(fmt.Errorf("download failed: %d", resp2.StatusCode))
		os.Exit(1)
	}

	_, err = io.Copy(out, resp2.Body)
	if err != nil {
		logErr(err)
		os.Exit(1)
	}

	logInfo("Download complete.")

	// 4. Replace system MMDB
	logInfo("Replacing old MMDB...")
	if err := os.Rename(tmpMMDB, saveMMDB); err != nil {
		logErr(err)
		os.Exit(1)
	}

	// 5. Parse MMDB and extract CN networks
	logInfo("Parsing MMDB and generating nftables sets...")

	db, err := maxminddb.Open(saveMMDB)
	if err != nil {
		logErr(err)
		os.Exit(1)
	}
	defer db.Close()

	var cnIPv4 []string
	var cnIPv6 []string

	// Iterate over all networks
	networks := db.Networks(maxminddb.SkipAliasedNetworks)
	for networks.Next() {
		var rec CountryRecord
		network, err := networks.Network(&rec)
		if err != nil {
			continue
		}

		if rec.Country.ISOCode == "CN" {
			_, ipNet, err := net.ParseCIDR(network.String())
			if err != nil {
				continue
			}

			if ipNet.IP.To4() != nil {
				cnIPv4 = append(cnIPv4, ipNet.String())
			} else {
				cnIPv6 = append(cnIPv6, ipNet.String())
			}
		}
	}

	// 6. Write nftables set files
	writeSetFile(outCN4, "cn4", "ipv4_addr", cnIPv4)
	writeSetFile(outCN6, "cn6", "ipv6_addr", cnIPv6)

	logInfo("Generated:")
	logInfo(fmt.Sprintf("- %s (%d IPv4 ranges)", outCN4, len(cnIPv4)))
	logInfo(fmt.Sprintf("- %s (%d IPv6 ranges)", outCN6, len(cnIPv6)))

	// 7. Reload nftables
	logInfo("Reloading nftables...")
	cmd := exec.Command("systemctl", "restart", "nftables")
	if out, err := cmd.CombinedOutput(); err != nil {
		logErr(fmt.Errorf("systemctl output: %s", string(out)))
		os.Exit(1)
	}

	logInfo("Done.")
}

func writeSetFile(path, setName, addrType string, items []string) {
	f, err := os.Create(path)
	if err != nil {
		logErr(err)
		os.Exit(1)
	}
	defer f.Close()

	fmt.Fprintf(f, "set %s {\n", setName)
	fmt.Fprintf(f, "    type %s\n", addrType)
	fmt.Fprintf(f, "    flags interval\n")
	fmt.Fprintf(f, "    elements = {\n")

	for _, n := range items {
		fmt.Fprintf(f, "        %s,\n", n)
	}

	fmt.Fprintf(f, "    }\n}\n")
}
