package serverinstall

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The places a server lives in, as the service sees them. Options.Prefix moves
// where installation writes them to (tests, image builds); it never appears in
// a unit or a configuration file.
const (
	BinPath = "/usr/local/bin/ctx"
	EtcDir  = "/etc/ghosttree"
	// EtcDirMode lets the service's dynamic user walk to the files that hold no
	// secret (llm.json); nobody can list the directory. Every file in it that
	// does hold one is 0600 or 0640 and root's.
	EtcDirMode = 0o711
	UnitDir    = "/etc/systemd/system"
	StateDir   = "/var/lib/ghosttree"
	DBPath     = StateDir + "/ghosttree.db"
	BackupDir  = StateDir + "/backups"
	SecretKey  = EtcDir + "/secret-key"
	ServerEnv  = EtcDir + "/server.env"
	LLMConfig  = EtcDir + "/llm.json"
	LLMKey     = EtcDir + "/llm-key"
	ProxyDir   = EtcDir + "/proxy"
	ServiceID  = "ghosttree.service"
	BackupID   = "ghosttree-backup.service"
	BackupTime = "ghosttree-backup.timer"
	// ServiceUser is the name the unit's dynamic user carries; maintenance
	// commands run as that user in a transient unit (deploy/README.md).
	ServiceUser = "ghosttree"
	DefaultPort = "8474"
)

// Options are the answers the installation needs. Zero values are the defaults.
type Options struct {
	// Prefix is prepended to every path installation writes (no systemd calls
	// are made differently): empty for the real system.
	Prefix string
	// Domain is the public name of the server; empty keeps the server on this
	// machine (http://127.0.0.1:8474), the way the quick start in the README does.
	Domain string
	// TLS is "proxy" (default: the server speaks http on Listen behind a proxy
	// of yours, configuration for Caddy and nginx is written) or "builtin" (the
	// server fetches its own certificate from Let's Encrypt).
	TLS    string
	Listen string // default 127.0.0.1:8474
	Admin  string // name of the first account, default admin
	// Email is the contact address for Let's Encrypt (builtin TLS), optional.
	Email string
	// Language of the interface: "", de or en.
	Language string
	// CtxBinary is the ctx to install as /usr/local/bin/ctx; default: the one running.
	CtxBinary string
	// AI is the optional model provider of the server.
	AI AIOptions
	// Force overwrites server.env and the units that exist.
	Force bool
	// Docker sets the server up as containers (compose.yaml in Dir) instead of
	// as a systemd service.
	Docker bool
	// Dir is where the Docker setup lives; default /opt/ghosttree for root,
	// ~/ghosttree otherwise.
	Dir string
	// Image is the image to run instead of the one the signed release names.
	Image string
	// ImageRepo is where the release's image is pulled from, default
	// ghcr.io/deadweight-labs/ghosttree; a mirror works, since the digest from the
	// signed release decides what is run.
	ImageRepo string
	// Version is the version of the ctx that runs the installation, which names
	// the default image.
	Version string
	// Subnet and Gateway are the bridge of the Docker setup; chosen at the
	// installation among the ranges no other Docker network uses.
	Subnet, Gateway string
}

// AIOptions describe an optional connection to a model provider. The key is
// read from a file; there is deliberately no way to pass it as an argument.
type AIOptions struct {
	Format  string // anthropic or openai; empty: no connection
	BaseURL string
	Model   string
	KeyFile string
}

func (a AIOptions) enabled() bool { return a.Format != "" }

func (o Options) listen() string { return orDefault(o.Listen, "127.0.0.1:"+DefaultPort) }

// ListenAddr is where the server listens, the default if none was given.
func (o Options) ListenAddr() string { return o.listen() }
func (o Options) tls() string        { return orDefault(o.TLS, "proxy") }
func (o Options) admin() string      { return orDefault(o.Admin, "admin") }

func (o Options) path(abs string) string {
	if o.Prefix == "" {
		return abs
	}
	return filepath.Join(o.Prefix, abs)
}

// A DNS name in lower case: labels of letters, digits and hyphens, at least two
// of them, the last starting with a letter. Everything else (a newline, a brace,
// a semicolon, a quote, a space) would end up in server.env, the Caddyfile and
// nginx.conf, so only what is on the list gets through.
var domainRE = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// An address for Let's Encrypt: a plain local part and a DNS name.
var emailRE = regexp.MustCompile(`(?i)^[a-z0-9._%+-]{1,64}@(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// A host name or address in --listen.
var listenHostRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)

func validDomain(d string) bool { return len(d) <= 253 && domainRE.MatchString(d) }
func validEmail(e string) bool  { return len(e) <= 254 && emailRE.MatchString(e) }

// Validate checks the answers before anything is written.
func (o Options) Validate() error {
	if o.Domain != "" && !validDomain(o.Domain) {
		return fmt.Errorf("--domain must be a DNS name in lower case like ghosttree.example.com (letters, digits, hyphens and dots; no scheme, port or path), got %q", o.Domain)
	}
	if o.Email != "" && !validEmail(o.Email) {
		return fmt.Errorf("--email must be a plain address like ops@example.com, got %q", o.Email)
	}
	switch o.tls() {
	case "proxy":
	case "builtin":
		if o.Domain == "" {
			return fmt.Errorf("--tls builtin needs --domain: the certificate is issued for that name")
		}
	default:
		return fmt.Errorf("--tls must be proxy or builtin, got %q", o.TLS)
	}
	host, port, err := net.SplitHostPort(o.listen())
	if n, perr := strconv.Atoi(port); err != nil || perr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return fmt.Errorf("--listen must be host:port, got %q", o.Listen)
	}
	if host != "" && net.ParseIP(host) == nil && !listenHostRE.MatchString(host) {
		return fmt.Errorf("--listen must be host:port with an address or a plain host name, got %q", o.Listen)
	}
	if o.Domain != "" && o.tls() == "proxy" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("--listen %s is not on this machine only: behind a proxy the server must listen on loopback; put the proxy on this machine, or set GHOSTTREE_LISTEN and GHOSTTREE_TRUSTED_PROXIES in %s by hand", o.listen(), ServerEnv)
	}
	if n := o.admin(); strings.TrimSpace(n) != n || n == "" || strings.ContainsAny(n, "\n\r\t/\\") || strings.IndexFunc(n, unicode.IsControl) >= 0 || len(n) > 64 {
		return fmt.Errorf("--admin must be a plain name, got %q", n)
	}
	switch o.Language {
	case "", "de", "en":
	default:
		return fmt.Errorf("--language must be de or en, got %q", o.Language)
	}
	if o.AI.enabled() {
		if o.AI.Format != "anthropic" && o.AI.Format != "openai" {
			return fmt.Errorf("--ai must be anthropic or openai, got %q", o.AI.Format)
		}
		if o.AI.KeyFile == "" {
			return fmt.Errorf("--ai needs --ai-key-file: the key is read from a file, never from an argument")
		}
		if o.AI.Format == "openai" && o.AI.BaseURL == "" {
			return fmt.Errorf("--ai openai needs --ai-base-url")
		}
	}
	return nil
}

// PublicURL is the address people use: https://<domain>, or http://<listen>
// for a server that stays on this machine.
func (o Options) PublicURL() string {
	if o.Domain != "" {
		return "https://" + o.Domain
	}
	return "http://" + localAddr(o.listen())
}

// localAddr turns a listen address into one a client on this machine can dial.
func localAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// ServerEnvText is /etc/ghosttree/server.env: the settings the unit passes on
// to ctx serve. Access control is switched on: a server that people reach over
// a network refuses by role from the first day.
func (o Options) ServerEnvText() string {
	var b strings.Builder
	b.WriteString("# Written by ctx server install. Settings of the ghosttree service; see\n")
	b.WriteString("# docs/operations.md. Restart after changing: systemctl restart ghosttree\n")
	fmt.Fprintf(&b, "GHOSTTREE_LISTEN=%s\n", o.listen())
	b.WriteString("GHOSTTREE_ENFORCE_ACCESS=1\n")
	if o.Domain != "" {
		fmt.Fprintf(&b, "GHOSTTREE_PUBLIC_URL=%s\n", o.PublicURL())
	}
	if o.tls() == "builtin" {
		fmt.Fprintf(&b, "GHOSTTREE_TLS_ACME_HOST=%s\n", o.Domain)
		if o.Email != "" {
			fmt.Fprintf(&b, "GHOSTTREE_TLS_ACME_EMAIL=%s\n", o.Email)
		}
		b.WriteString("# No proxy in front: forwarding headers are nobody's to set.\nGHOSTTREE_TRUSTED_PROXIES=none\n")
	} else if o.Domain != "" {
		b.WriteString("# The proxy of the generated configuration runs on this machine. A proxy on\n")
		b.WriteString("# another machine: list its address here and listen on a private address.\n")
		b.WriteString("GHOSTTREE_TRUSTED_PROXIES=127.0.0.1\n")
	}
	if o.Language != "" {
		fmt.Fprintf(&b, "GHOSTTREE_LANGUAGE=%s\n", o.Language)
	}
	return b.String()
}

// serviceLimits are the finite snapshot and writer limits of deploy/ghosttree.service.
const serviceLimits = `Environment=GHOSTTREE_SNAPSHOT_MAX_ENTRY_BYTES=4194304
Environment=GHOSTTREE_SNAPSHOT_MAX_ENTRIES=20000
Environment=GHOSTTREE_SNAPSHOT_MAX_PAYLOAD_BYTES=134217728
Environment=GHOSTTREE_SNAPSHOT_MAX_HEAD_BYTES=32768
Environment=GHOSTTREE_SNAPSHOT_MAX_LOGICAL_BYTES=167772160
Environment=GHOSTTREE_SNAPSHOT_MAX_PROJECT_COUNT=1000
Environment=GHOSTTREE_SNAPSHOT_MAX_PROJECT_BYTES=8589934592
Environment=GHOSTTREE_SNAPSHOT_MAX_STORE_COUNT=10000
Environment=GHOSTTREE_SNAPSHOT_MAX_STORE_BYTES=68719476736
Environment=GHOSTTREE_WRITER_MAX_OPERATIONS=1024
Environment=GHOSTTREE_WRITER_MAX_BYTES=268435456
Environment=GHOSTTREE_WRITER_MAX_BATCH=64
Environment=GHOSTTREE_WRITER_READ_CONNECTIONS=3
`

const serviceExec = "ExecStart=" + BinPath + " serve --db " + DBPath + " --listen ${GHOSTTREE_LISTEN}" +
	" --snapshot-max-entry-bytes ${GHOSTTREE_SNAPSHOT_MAX_ENTRY_BYTES} --snapshot-max-entries ${GHOSTTREE_SNAPSHOT_MAX_ENTRIES}" +
	" --snapshot-max-payload-bytes ${GHOSTTREE_SNAPSHOT_MAX_PAYLOAD_BYTES} --snapshot-max-head-bytes ${GHOSTTREE_SNAPSHOT_MAX_HEAD_BYTES}" +
	" --snapshot-max-logical-bytes ${GHOSTTREE_SNAPSHOT_MAX_LOGICAL_BYTES} --snapshot-max-project-count ${GHOSTTREE_SNAPSHOT_MAX_PROJECT_COUNT}" +
	" --snapshot-max-project-bytes ${GHOSTTREE_SNAPSHOT_MAX_PROJECT_BYTES} --snapshot-max-store-count ${GHOSTTREE_SNAPSHOT_MAX_STORE_COUNT}" +
	" --snapshot-max-store-bytes ${GHOSTTREE_SNAPSHOT_MAX_STORE_BYTES} --writer-max-operations ${GHOSTTREE_WRITER_MAX_OPERATIONS}" +
	" --writer-max-bytes ${GHOSTTREE_WRITER_MAX_BYTES} --writer-max-batch ${GHOSTTREE_WRITER_MAX_BATCH}" +
	" --writer-read-connections ${GHOSTTREE_WRITER_READ_CONNECTIONS}\n"

// ServiceText is /etc/systemd/system/ghosttree.service: deploy/ghosttree.service
// (a test keeps the two in step) plus the server key as a credential and, when
// they are wanted, the model provider and the right to bind ports 80 and 443.
func (o Options) ServiceText() string {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=ghosttree context server\nAfter=network-online.target\nWants=network-online.target\n\n")
	b.WriteString("[Service]\nUser=" + ServiceUser + "\nDynamicUser=yes\nStateDirectory=ghosttree\n")
	b.WriteString("Environment=GHOSTTREE_LISTEN=127.0.0.1:" + DefaultPort + "\n")
	b.WriteString(serviceLimits)
	b.WriteString("EnvironmentFile=-" + ServerEnv + "\n")
	b.WriteString("# The key that seals what the server keeps secret (AI keys); see deploy/README.md.\n")
	b.WriteString("LoadCredential=ghosttree-secret-key:" + SecretKey + "\n")
	if o.AI.enabled() {
		// llm.json holds no secret (the key is the credential next to it), and the
		// service opens it itself, as its dynamic user: /etc/ghosttree can be
		// entered by everybody (Install sets EtcDirMode), the file can be read by
		// everybody, and the key stays a root-only file that systemd hands over.
		b.WriteString("LoadCredential=llm-key:" + LLMKey + "\nEnvironment=GHOSTTREE_LLM_CONFIG=" + LLMConfig + "\n")
	}
	if o.tls() == "builtin" {
		b.WriteString("# Ports 80 and 443 for the server's own certificate, and nothing else.\nAmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\n")
	} else {
		b.WriteString("# Behind a proxy the server needs no privilege at all.\nCapabilityBoundingSet=\n")
	}
	b.WriteString(serviceHardening)
	b.WriteString("UMask=0077\n")
	b.WriteString(serviceExec)
	b.WriteString("TimeoutStopSec=330\nRestart=on-failure\n\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

// serviceHardening keeps a compromised server from doing more than serving and
// writing its own directory: no devices, no kernel settings, modules or logs, no
// control groups, no namespaces, only network and unix sockets, no writable
// executable memory, and the system calls of an ordinary service. DynamicUser
// already gives it a private, read-only view of the file system. It is for the
// server and the backup, which only read and write the database; the gardener's
// units call git and claude and are looked at separately (deploy/README.md).
const serviceHardening = `NoNewPrivileges=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
`

// BackupServiceText is the unit the backup timer starts: ctx db backup as the
// service's own user, keeping the newest fourteen copies.
func BackupServiceText() string {
	return "[Unit]\nDescription=ghosttree database backup\nAfter=" + ServiceID + "\n\n" +
		"[Service]\nType=oneshot\nUser=" + ServiceUser + "\nDynamicUser=yes\nStateDirectory=ghosttree\nUMask=0077\n" +
		"CapabilityBoundingSet=\n" + serviceHardening +
		"ExecStart=" + BinPath + " db backup --db " + DBPath + " --out " + BackupDir + " --keep 14\n"
}

// BackupTimerText runs the backup every night.
func BackupTimerText() string {
	return "[Unit]\nDescription=ghosttree database backup every night\n\n" +
		"[Timer]\nOnCalendar=*-*-* 03:30\nRandomizedDelaySec=15m\nPersistent=true\nUnit=" + BackupID + "\n\n[Install]\nWantedBy=timers.target\n"
}

// LLMConfigText is /etc/ghosttree/llm.json for the optional model provider.
func (a AIOptions) LLMConfigText() string {
	cfg := struct {
		Format     string `json:"format"`
		BaseURL    string `json:"base_url,omitempty"`
		Model      string `json:"model,omitempty"`
		Credential string `json:"credential"`
	}{a.Format, a.BaseURL, a.Model, "llm-key"}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		panic(err) // strings only
	}
	return string(b) + "\n"
}

// CaddyfileText is a site block for Caddy, which fetches the certificate itself
// and sets the forwarding headers the server needs.
func (o Options) CaddyfileText() string {
	host := orDefault(o.Domain, "ghosttree.example.com")
	return fmt.Sprintf(`# Caddy in front of ghosttree (written by ctx server install).
# Copy it into your Caddyfile, or save it as /etc/caddy/conf.d/ghosttree.caddy
# when your Caddyfile has "import conf.d/*", then: systemctl reload caddy
%s {
	encode zstd gzip
	reverse_proxy %s {
		# Streams (live pages) must not wait for a full buffer.
		flush_interval -1
	}
}
`, host, localAddr(o.listen()))
}

// NginxConfText is a server block for nginx. The certificate paths are those of
// certbot; change them if yours differ. The proxy overwrites the forwarding
// headers instead of appending to what a client sent.
func (o Options) NginxConfText() string {
	host := orDefault(o.Domain, "ghosttree.example.com")
	return fmt.Sprintf(`# nginx in front of ghosttree (written by ctx server install).
# Save as /etc/nginx/conf.d/ghosttree.conf, get a certificate
# (certbot certonly --nginx -d %[1]s), then: nginx -t && systemctl reload nginx
server {
    listen 80;
    server_name %[1]s;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    http2 on;
    server_name %[1]s;
    ssl_certificate     /etc/letsencrypt/live/%[1]s/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/%[1]s/privkey.pem;

    # Context snapshots can be large (the server's own limit is 128 MiB).
    client_max_body_size 256m;

    location / {
        proxy_pass http://%[2]s;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
        # Live pages and long requests.
        proxy_buffering off;
        proxy_read_timeout 1h;
    }
}
`, host, localAddr(o.listen()))
}

// writeFile writes content to path (parents made, mode as given), replacing an
// existing file atomically. It reports whether it wrote.
func writeFile(path string, content []byte, mode os.FileMode, replace bool) (bool, error) {
	if _, err := os.Lstat(path); err == nil && !replace {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gt-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}
