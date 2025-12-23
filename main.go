package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Source struct {
	Name string
	URL  string
}

var defaultSources = []Source{
	// GitHub raw
	{Name: "TheSpeedX-http", URL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt"},
	{Name: "TheSpeedX-https", URL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/https.txt"},
	{Name: "monosans-http", URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt"},
	{Name: "monosans-https", URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/https.txt"},
	{Name: "clarketm-raw", URL: "https://raw.githubusercontent.com/clarketm/proxy-list/master/proxy-list-raw.txt"},
	{Name: "jetkai-http", URL: "https://raw.githubusercontent.com/jetkai/proxy-list/main/online-proxies/txt/proxies-http.txt"},
	{Name: "suny9577-raw", URL: "https://raw.githubusercontent.com/sunny9577/proxy-scraper/master/proxies.txt"},
	{Name: "roosterkid-https", URL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main/HTTPS_RAW.txt"},
	{Name: "opsxcq-raw", URL: "https://raw.githubusercontent.com/opsxcq/proxy-list/master/list.txt"},
	{Name: "proxy4parsing-http", URL: "https://raw.githubusercontent.com/proxy4parsing/proxy-list/main/http.txt"},
	{Name: "rdavydov-http", URL: "https://raw.githubusercontent.com/rdavydov/proxy-list/main/proxies/http.txt"},
	{Name: "rdavydov-anon-http", URL: "https://raw.githubusercontent.com/rdavydov/proxy-list/main/proxies_anonymous/http.txt"},
	{Name: "proxifly-http", URL: "https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/protocols/http/data.txt"},
	{Name: "proxifly-https", URL: "https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/protocols/https/data.txt"},

	// APIs
	{Name: "proxyscrape-http", URL: "https://api.proxyscrape.com/v2/?request=getproxies&protocol=http&timeout=10000&country=all&ssl=all&anonymity=all"},
	{Name: "proxyscrape-https", URL: "https://api.proxyscrape.com/v2/?request=getproxies&protocol=https&timeout=10000&country=all&ssl=all&anonymity=all"},
	{Name: "proxy-list-download-http", URL: "https://www.proxy-list.download/api/v1/get?type=http"},
	{Name: "proxy-list-download-https", URL: "https://www.proxy-list.download/api/v1/get?type=https"},
	{Name: "proxyscan-http", URL: "https://www.proxyscan.io/download?type=http"},
	{Name: "proxyscan-https", URL: "https://www.proxyscan.io/download?type=https"},
	{Name: "openproxylist-http", URL: "https://api.openproxylist.xyz/http.txt"},
	{Name: "openproxylist-https", URL: "https://api.openproxylist.xyz/https.txt"},
	{Name: "proxyspace-http", URL: "https://proxyspace.pro/http.txt"},
	{Name: "spysme", URL: "http://spys.me/proxy.txt"},
	{Name: "rootjazz", URL: "http://rootjazz.com/proxies/proxies.txt"},
}

var proxyRegex = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}:\d{2,5}\b`)

type stats struct {
	fetchedOK uint64
	linesRead uint64
	found     uint64
	enqueued  uint64
	valid     uint64
}

func main() {
	var (
		outFile      = flag.String("out", "proxies.txt", "output file")
		sourcesFile  = flag.String("sources", "", "optional: path to sources file (one URL per line, optional 'name=URL')")
		mode         = flag.String("mode", "both", "validation mode: http | connect | both")
		workers      = flag.Int("workers", 300, "validator workers")
		fetchers     = flag.Int("fetchers", 20, "max concurrent fetches")
		maxValid     = flag.Int("max", 0, "stop after N valid proxies (0 = no limit)")
		totalTimeout = flag.Duration("total-timeout", 2*time.Minute, "total runtime timeout")
		httpTimeout  = flag.Duration("http-timeout", 20*time.Second, "http fetch timeout")
		dialTimeout  = flag.Duration("dial-timeout", 4*time.Second, "tcp dial timeout for validation")
		rwTimeout    = flag.Duration("rw-timeout", 4*time.Second, "read/write timeout for validation")
		testHost     = flag.String("test-host", "example.com", "host used for validation (GET and CONNECT)")
		userAgent    = flag.String("ua", "proxy-scraper/1.0 (+github)", "User-Agent for fetching lists")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *totalTimeout)
	defer cancel()

	sources := defaultSources
	if *sourcesFile != "" {
		custom, err := loadSourcesFile(*sourcesFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "failed to load sources:", err)
			os.Exit(1)
		}
		if len(custom) > 0 {
			sources = custom
		}
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	client := &http.Client{
		Timeout:   *httpTimeout,
		Transport: transport,
	}

	raw := make(chan string, 20000)
	jobs := make(chan string, 20000)
	valid := make(chan string, 20000)

	var st stats

	var seen sync.Map

	var fwg sync.WaitGroup
	sem := make(chan struct{}, *fetchers)

	for _, src := range sources {
		src := src
		fwg.Add(1)
		go func() {
			defer fwg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			fetchList(ctx, client, src, raw, &st, *userAgent)
		}()
	}

	go func() {
		fwg.Wait()
		close(raw)
	}()

	go func() {
		defer close(jobs)
		for p := range raw {
			if _, loaded := seen.LoadOrStore(p, struct{}{}); loaded {
				continue
			}
			atomic.AddUint64(&st.enqueued, 1)

			select {
			case jobs <- p:
			case <-ctx.Done():
				return
			}
		}
	}()

	var vwg sync.WaitGroup
	validCount := int64(0)

	for i := 0; i < *workers; i++ {
		vwg.Add(1)
		go func() {
			defer vwg.Done()
			for p := range jobs {
				if ctx.Err() != nil {
					return
				}
				ok := validateProxy(p, *mode, *testHost, *dialTimeout, *rwTimeout)
				if !ok {
					continue
				}

				atomic.AddUint64(&st.valid, 1)
				newCount := atomic.AddInt64(&validCount, 1)

				select {
				case valid <- p:
				case <-ctx.Done():
					return
				}

				if *maxValid > 0 && int(newCount) >= *maxValid {
					cancel()
					return
				}
			}
		}()
	}

	go func() {
		vwg.Wait()
		close(valid)
	}()

	var out []string
	for p := range valid {
		out = append(out, p)
	}
	sort.Strings(out)

	if err := writeLines(*outFile, out); err != nil {
		fmt.Fprintln(os.Stderr, "failed writing output:", err)
		os.Exit(1)
	}

	fmt.Printf("Done.\n")
	fmt.Printf("Sources: %d | fetched_ok: %d | lines: %d | found: %d | enqueued: %d | valid: %d | wrote: %d\n",
		len(sources),
		atomic.LoadUint64(&st.fetchedOK),
		atomic.LoadUint64(&st.linesRead),
		atomic.LoadUint64(&st.found),
		atomic.LoadUint64(&st.enqueued),
		atomic.LoadUint64(&st.valid),
		len(out),
	)
}

func fetchList(ctx context.Context, client *http.Client, src Source, out chan<- string, st *stats, userAgent string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/plain,*/*;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	atomic.AddUint64(&st.fetchedOK, 1)

	reader := bufio.NewReaderSize(resp.Body, 256*1024)
	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		atomic.AddUint64(&st.linesRead, 1)
		line := sc.Text()
		matches := proxyRegex.FindAllString(line, -1)
		if len(matches) == 0 {
			continue
		}
		for _, m := range matches {
			if !looksValidHostPort(m) {
				continue
			}
			atomic.AddUint64(&st.found, 1)
			select {
			case out <- m:
			case <-ctx.Done():
				return
			}
		}
	}
}

func looksValidHostPort(s string) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return false
	}
	return true
}

func validateProxy(proxy string, mode string, testHost string, dialTimeout, rwTimeout time.Duration) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "http":
		return validateHTTP(proxy, testHost, dialTimeout, rwTimeout)
	case "connect":
		return validateCONNECT(proxy, testHost, dialTimeout, rwTimeout)
	default:
		return validateHTTP(proxy, testHost, dialTimeout, rwTimeout) || validateCONNECT(proxy, testHost, dialTimeout, rwTimeout)
	}
}

func validateHTTP(proxyAddr, testHost string, dialTimeout, rwTimeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", proxyAddr, dialTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(rwTimeout))

	fmt.Fprintf(conn,
		"GET http://%s/ HTTP/1.1\r\nHost: %s\r\nUser-Agent: proxy-scraper/1.0\r\nConnection: close\r\n\r\n",
		testHost, testHost,
	)

	r := bufio.NewReaderSize(conn, 4096)
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(line)

	if strings.HasPrefix(line, "HTTP/1.1 ") || strings.HasPrefix(line, "HTTP/1.0 ") {
		parts := strings.Split(line, " ")
		if len(parts) >= 2 {
			code, err := strconv.Atoi(parts[1])
			if err == nil && code >= 200 && code < 400 {
				return true
			}
		}
	}
	return false
}

func validateCONNECT(proxyAddr, testHost string, dialTimeout, rwTimeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", proxyAddr, dialTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(rwTimeout))

	fmt.Fprintf(conn,
		"CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\nProxy-Connection: keep-alive\r\n\r\n",
		testHost, testHost,
	)

	r := bufio.NewReaderSize(conn, 4096)
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(line)

	if strings.HasPrefix(line, "HTTP/1.1 200") || strings.HasPrefix(line, "HTTP/1.0 200") {
		return true
	}
	return false
}

func writeLines(path string, lines []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 256*1024)
	for _, s := range lines {
		if _, err := w.WriteString(s + "\n"); err != nil {
			return err
		}
	}
	return w.Flush()
}

func loadSourcesFile(path string) ([]Source, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var out []Source
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := ""
		u := line

		if strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			name = strings.TrimSpace(parts[0])
			u = strings.TrimSpace(parts[1])
		}
		if _, err := url.ParseRequestURI(u); err != nil {
			continue
		}
		if name == "" {
			name = u
		}
		out = append(out, Source{Name: name, URL: u})
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}

func readAllAndExtract(r io.Reader) []string {
	b, _ := io.ReadAll(r)
	return proxyRegex.FindAllString(string(b), -1)
}


	vwg.Wait()
}
