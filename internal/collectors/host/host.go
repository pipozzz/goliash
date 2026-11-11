// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package host collects a Linux server: its operating system and the installed packages that matter, read
// from /etc/os-release and the dpkg, apk or RPM databases. It only reads files; it runs nothing on the host.
package host

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// PackageRegistry is the made-up registry of packages without an image: Goliash records their versions but
// looks for no releases.
const PackageRegistry = "pkg.goliash"

// Collector reads one host.
type Collector struct {
	root  string
	extra map[string]bool
	all   bool
	// osrelease is where the running kernel's release is read: the host's, also from inside the agent's container,
	// which shares the kernel.
	osrelease string
	kernels   []string // the kernel packages' versions, as the package database lists them

	// The host's packages for vulnerabilities, read again only when its package database changes.
	dbStamp string
	code    collectors.CodePackages
}

// New is the collectors.Factory for host targets.
func New(_ context.Context, t agentproto.Target) (collectors.Collector, error) {
	c := &Collector{root: "/", extra: map[string]bool{}, osrelease: "/proc/sys/kernel/osrelease"}
	if s := t.Host; s != nil {
		if s.Root != nil && *s.Root != "" {
			c.root = *s.Root
		}
		for _, p := range s.Packages {
			c.extra[strings.TrimSpace(p)] = true
		}
		c.all = s.AllPackages != nil && *s.AllPackages
	}
	if _, err := os.Stat(filepath.Join(c.root, "etc", "os-release")); err != nil {
		return nil, errors.New("host target: no etc/os-release under " + c.root + "; mount the host's root and set root")
	}
	return c, nil
}

// Collect reports the operating system and the packages.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	osr, err := readKeyValues(filepath.Join(c.root, "etc", "os-release"))
	if err != nil {
		return collectors.Result{}, err
	}
	res := collectors.Result{Complete: true}
	id, version := osr["ID"], osr["VERSION_ID"]
	pkgs, err := c.packages(ctx)
	if err != nil {
		res.Complete = false
		res.Errors = append(res.Errors, err.Error())
	}
	if id != "" && version != "" {
		w := workload("os/"+id, agentproto.HostOs, id, osImage(id)+":"+version)
		w.Maintenance = append(c.rebootRequired(pkgs), c.updatesAvailable()...)
		if code, ok := c.osPackages(ctx); ok {
			d := code.Digest
			w.CodeDigest = &d
			res.Code = append(res.Code, code)
		}
		res.Workloads = append(res.Workloads, w)
	}
	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	seen := map[string]bool{} // python3 and python3.12 are one Python
	for _, name := range names {
		image, known := packageImage(name)
		if !known && !c.extra[name] && !c.all {
			continue
		}
		v := upstreamVersion(pkgs[name])
		if v == "" || seen[image+":"+v] {
			continue
		}
		seen[image+":"+v] = true
		res.Workloads = append(res.Workloads, workload("pkg/"+name, agentproto.HostPackage, serviceName(name), image+":"+v))
	}
	return res, nil
}

// osPackages lists every package the host's database has, as an image's are listed, for its vulnerabilities: by
// digest, like a Lambda function's code, read again only when the database changes. Of the kernel's packages only
// the running kernel's are kept: an older kernel left installed does not run, and its fixed vulnerabilities would
// be reported for nothing.
func (c *Collector) osPackages(ctx context.Context) (collectors.CodePackages, bool) {
	files := map[string]string{"dpkg": "var/lib/dpkg/status", "apk": "lib/apk/db/installed"}
	for _, p := range rpmDBs {
		files["rpm:"+p] = p
	}
	stamp := ""
	found := map[string][]byte{}
	for _, k := range sortedKeys(files) {
		fi, err := os.Stat(filepath.Join(c.root, files[k]))
		if err != nil {
			continue
		}
		stamp += fmt.Sprintf("%s:%d:%d;", k, fi.Size(), fi.ModTime().UnixNano())
		found[k] = nil
	}
	if stamp == "" {
		return collectors.CodePackages{}, false
	}
	running := ""
	if b, err := os.ReadFile(c.osrelease); err == nil {
		running = strings.TrimSpace(string(b))
	}
	stamp += "kernel:" + running
	if stamp == c.dbStamp {
		return c.code, c.code.Digest != ""
	}
	read := func(rel string) []byte {
		b, err := readLimited(filepath.Join(c.root, rel), maxRPMDB)
		if err != nil {
			return nil
		}
		return b
	}
	osr, _ := os.ReadFile(filepath.Join(c.root, "etc", "os-release")) //nolint:gosec // a fixed file under the host's root
	var dpkg, apk, rpm []byte
	if _, ok := found["dpkg"]; ok {
		dpkg = read(files["dpkg"])
	}
	if _, ok := found["apk"]; ok {
		apk = read(files["apk"])
	}
	for _, p := range rpmDBs {
		if _, ok := found["rpm:"+p]; ok && rpm == nil {
			rpm = read(p)
		}
	}
	purls, err := registry.OSPurls(ctx, osr, dpkg, nil, apk, rpm)
	code := collectors.CodePackages{}
	if err != nil {
		code.Error = err.Error()
	}
	purls = runningKernelOnly(purls, running)
	if len(purls) == 0 && code.Error == "" {
		return collectors.CodePackages{}, false
	}
	sum := sha256.Sum256([]byte(strings.Join(purls, "\n")))
	code.Digest, code.Purls = "sha256:"+hex.EncodeToString(sum[:]), purls
	c.dbStamp, c.code = stamp, code
	return code, true
}

// runningKernelOnly leaves out the kernel's packages but the running kernel's (linux-image-6.8.0-1015-aws when it
// runs, kernel-core 5.14.0-503.11.1.el9_5 for 5.14.0-503.11.1.el9_5.x86_64), and kernel headers, which run nowhere.
// Without the running kernel known, every kernel package stays.
func runningKernelOnly(purls []string, running string) []string {
	if running == "" {
		return purls
	}
	out := purls[:0:0]
	for _, p := range purls {
		name, version := purlNameVersion(p)
		switch {
		case name == "linux-libc-dev" || strings.HasPrefix(name, "linux-headers") || name == "kernel-headers" || name == "kernel-devel":
			continue
		case kernelRelease(name) != "":
			if kernelRelease(name) != running {
				continue // another kernel, installed and not running
			}
		case name == "kernel" || name == "kernel-core" || name == "kernel-modules" || name == "kernel-default":
			if v := strings.TrimPrefix(version, "0:"); !strings.HasPrefix(running, v) {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// kernelRelease is the release a Debian or Ubuntu kernel package is for (linux-image-6.8.0-1015-aws is
// 6.8.0-1015-aws); "" for other packages, and for meta packages such as linux-image-aws.
func kernelRelease(name string) string {
	for _, prefix := range []string{"linux-image-unsigned-", "linux-image-", "linux-modules-extra-", "linux-modules-"} {
		if r, ok := strings.CutPrefix(name, prefix); ok && r != "" && r[0] >= '0' && r[0] <= '9' {
			return r
		}
	}
	return ""
}

// purlNameVersion is a package URL's name and version: pkg:deb/ubuntu/openssl@3.0.13-0ubuntu3?… is openssl and
// 3.0.13-0ubuntu3.
func purlNameVersion(p string) (string, string) {
	p, _, _ = strings.Cut(p, "?")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	name, version, _ := strings.Cut(p, "@")
	if v, err := url.PathUnescape(version); err == nil {
		version = v
	}
	return name, version
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// rebootRequired says what waits for a reboot: Debian's and Ubuntu's /var/run/reboot-required (with the packages
// that asked for it), or a kernel installed newer than the one running. Nothing when neither is known.
func (c *Collector) rebootRequired(pkgs map[string]string) []string {
	if _, err := os.Stat(filepath.Join(c.root, "var", "run", "reboot-required")); err == nil {
		m := "reboot required"
		if b, err := os.ReadFile(filepath.Join(c.root, "var", "run", "reboot-required.pkgs")); err == nil { //nolint:gosec // a fixed file under the host's root
			if names := uniqueLines(string(b)); len(names) > 0 {
				m += ": " + strings.Join(names, ", ") + " updated"
			}
		}
		return []string{m}
	}
	running := ""
	if b, err := os.ReadFile(c.osrelease); err == nil {
		running = strings.TrimSpace(string(b))
	}
	if running == "" {
		return nil
	}
	kernels := c.kernels
	for name := range pkgs { // linux-image-6.8.0-1015-aws: the release is in the name
		if r, ok := strings.CutPrefix(name, "linux-image-"); ok && r != "" && r[0] >= '0' && r[0] <= '9' {
			kernels = append(kernels, r)
		}
	}
	newest := ""
	for _, k := range kernels {
		if newest == "" || compareRelease(k, newest) > 0 {
			newest = k
		}
	}
	if newest == "" || strings.HasPrefix(running, newest) || compareRelease(newest, running) <= 0 {
		return nil
	}
	return []string{"reboot required: kernel " + newest + " installed, " + running + " runs"}
}

// updatesAvailable reads what Ubuntu's update-notifier counted last: "12 updates can be applied immediately. 5 of
// these updates are standard security updates." It says nothing when the file is not there or counts none.
func (c *Collector) updatesAvailable() []string {
	b, err := os.ReadFile(filepath.Join(c.root, "var", "lib", "update-notifier", "updates-available")) //nolint:gosec // a fixed file under the host's root
	if err != nil {
		return nil
	}
	all, security := 0, 0
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		var n int
		switch {
		case strings.Contains(l, "can be applied immediately") || strings.Contains(l, "can be installed immediately"):
			if _, err := fmt.Sscan(l, &n); err == nil {
				all = n
			}
		case strings.Contains(l, "security update"):
			if _, err := fmt.Sscan(l, &n); err == nil {
				security = n
			}
		}
	}
	if all == 0 {
		return nil
	}
	m := fmt.Sprintf("package updates: %d waiting", all)
	if security > 0 {
		m += fmt.Sprintf(", %d of them security", security)
	}
	return []string{m}
}

// uniqueLines are a file's non-empty lines, each once, in order.
func uniqueLines(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// compareRelease orders kernel releases by their numbers: 6.8.0-1015-aws after 6.8.0-1012-aws,
// 5.14.0-503.11.1.el9_5 after 5.14.0-427.13.1.el9_4. Letters between them do not count.
func compareRelease(a, b string) int {
	na, nb := releaseNumbers(a), releaseNumbers(b)
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] != nb[i] {
			if na[i] < nb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(na) < len(nb):
		return -1
	case len(na) > len(nb):
		return 1
	}
	return 0
}

func releaseNumbers(s string) []int {
	var out []int
	n, in := 0, false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n, in = n*10+int(r-'0'), true
			continue
		}
		if in {
			out = append(out, n)
			n, in = 0, false
		}
	}
	if in {
		out = append(out, n)
	}
	return out
}

func workload(id string, kind agentproto.WorkloadKind, name, image string) agentproto.Workload {
	running := 1
	return agentproto.Workload{
		ID: id, Kind: kind, Name: name, DesiredReplicas: &running,
		Containers: []agentproto.Container{{Name: name, Image: image, Running: 1}},
	}
}

// packages reads the installed packages and their versions from dpkg, apk or RPM's SQLite database.
func (c *Collector) packages(ctx context.Context) (map[string]string, error) {
	if f, err := os.Open(filepath.Join(c.root, "var", "lib", "dpkg", "status")); err == nil {
		defer func() { _ = f.Close() }()
		return parseDpkg(f)
	}
	if f, err := os.Open(filepath.Join(c.root, "lib", "apk", "db", "installed")); err == nil {
		defer func() { _ = f.Close() }()
		return parseApk(f)
	}
	for _, p := range rpmDBs {
		b, err := readLimited(filepath.Join(c.root, p), maxRPMDB)
		if err != nil {
			continue
		}
		ps, err := registry.RPMPackages(ctx, b)
		if err != nil {
			return nil, fmt.Errorf("reading the RPM database %s: %w", p, err)
		}
		out := make(map[string]string, len(ps))
		for _, pk := range ps {
			out[pk.Name] = pk.Version
			if pk.Name == "kernel" || pk.Name == "kernel-core" || pk.Name == "kernel-default" {
				c.kernels = append(c.kernels, pk.Version)
			}
		}
		return out, nil
	}
	if _, err := os.Stat(filepath.Join(c.root, "var", "lib", "rpm", "Packages")); err == nil {
		return nil, errors.New("the RPM database is a Berkeley DB (RHEL 8, Amazon Linux 2 and older), which is not read; packages are not reported")
	}
	return nil, errors.New("no dpkg, apk or RPM package database found; packages are not reported")
}

// rpmDBs are where RPM's SQLite database is, under the root: the usual place, and Fedora's newer one.
var rpmDBs = []string{"var/lib/rpm/rpmdb.sqlite", "usr/lib/sysimage/rpm/rpmdb.sqlite"}

// maxRPMDB bounds the RPM database read: a server's is a few tens of megabytes at most.
const maxRPMDB = 256 << 20

// readLimited reads a file of at most limit bytes. The RPM database is copied, not opened in place: rpm may be
// writing it, and SQLite would want to write beside it.
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // a fixed file under the root the target names: reading the host is the point
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s is larger than %d MB", path, limit>>20)
	}
	return b, err
}

// parseDpkg reads /var/lib/dpkg/status: stanzas of "Field: value", installed when Status ends in "installed".
func parseDpkg(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	var name, version, status string
	flush := func() {
		if name != "" && version != "" && strings.HasSuffix(status, " installed") {
			out[name] = version
		}
		name, version, status = "", "", ""
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "Package: "):
			name = strings.TrimPrefix(line, "Package: ")
		case strings.HasPrefix(line, "Version: "):
			version = strings.TrimPrefix(line, "Version: ")
		case strings.HasPrefix(line, "Status: "):
			status = strings.TrimPrefix(line, "Status: ")
		}
	}
	flush()
	return out, sc.Err()
}

// parseApk reads /lib/apk/db/installed: stanzas of "K:value" lines, P the package and V its version.
func parseApk(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	var name, version string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if name != "" && version != "" {
				out[name] = version
			}
			name, version = "", ""
		case strings.HasPrefix(line, "P:"):
			name = line[2:]
		case strings.HasPrefix(line, "V:"):
			version = line[2:]
		}
	}
	if name != "" && version != "" {
		out[name] = version
	}
	return out, sc.Err()
}

func readKeyValues(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // a fixed file under the root the target names: reading the host is the point
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return out, sc.Err()
}

var leadingVersion = regexp.MustCompile(`^\d+(\.\d+)*`)

// upstreamVersion turns a distribution's package version into the upstream one Goliash compares:
// "1:1.18.0-6ubuntu14.4" -> 1.18.0, "15.8-0+deb12u1" -> 15.8, "3.3.2-r0" -> 3.3.2.
func upstreamVersion(v string) string {
	if _, after, ok := strings.Cut(v, ":"); ok {
		v = after
	}
	return leadingVersion.FindString(v)
}

// osImage is the image of an operating system, so its releases and end of life are known.
func osImage(id string) string {
	if img, ok := map[string]string{
		"ubuntu": "docker.io/library/ubuntu", "debian": "docker.io/library/debian", "alpine": "docker.io/library/alpine",
		"rocky": "docker.io/library/rockylinux", "almalinux": "docker.io/library/almalinux",
		"amzn": "docker.io/library/amazonlinux", "fedora": "docker.io/library/fedora", "centos": "docker.io/library/centos",
		"opensuse-leap": "docker.io/opensuse/leap",
	}[id]; ok {
		return img
	}
	return PackageRegistry + "/os/" + id
}

// knownPackages maps packages worth watching on a server to the image of the same software, where one exists;
// the others are recorded under the package registry.
var knownPackages = map[string]string{
	"nginx": "docker.io/library/nginx", "nginx-core": "docker.io/library/nginx", "apache2": "docker.io/library/httpd",
	"httpd": "docker.io/library/httpd", "haproxy": "docker.io/library/haproxy", "varnish": "docker.io/library/varnish",
	"redis-server": "docker.io/library/redis", "redis": "docker.io/library/redis", "memcached": "docker.io/library/memcached",
	"mysql-server": "docker.io/library/mysql", "mariadb-server": "docker.io/library/mariadb",
	"mongodb-org-server": "docker.io/library/mongo", "rabbitmq-server": "docker.io/library/rabbitmq",
	"elasticsearch": "docker.elastic.co/elasticsearch/elasticsearch", "grafana": "docker.io/grafana/grafana",
	"prometheus": "docker.io/prom/prometheus", "jenkins": "docker.io/jenkins/jenkins", "gitlab-ce": "docker.io/gitlab/gitlab-ce",
	"gitlab-ee": "docker.io/gitlab/gitlab-ee", "docker-ce": "docker.io/library/docker", "nodejs": "docker.io/library/node",
	"python3": "docker.io/library/python", "golang-go": "docker.io/library/golang", "ruby": "docker.io/library/ruby",
	"traefik": "docker.io/library/traefik", "vault": "docker.io/hashicorp/vault", "consul": "docker.io/hashicorp/consul",
	"nomad": "docker.io/hashicorp/nomad", "keycloak": "quay.io/keycloak/keycloak",
	"openssl": PackageRegistry + "/openssl", "libssl3": PackageRegistry + "/openssl", "openssh-server": PackageRegistry + "/openssh",
	"sudo": PackageRegistry + "/sudo", "containerd.io": PackageRegistry + "/containerd", "kubelet": PackageRegistry + "/kubelet",
	// As RPM-based distributions name them.
	"openssl-libs": PackageRegistry + "/openssl", "postgresql-server": "docker.io/library/postgres",
	"java-17-openjdk-headless": "docker.io/library/eclipse-temurin", "java-21-openjdk-headless": "docker.io/library/eclipse-temurin",
	"java-11-openjdk-headless": "docker.io/library/eclipse-temurin", "containerd": PackageRegistry + "/containerd",
}

var versionedPackage = regexp.MustCompile(`^(postgresql|openjdk|php|tomcat|python)-?(\d+(\.\d+)?)(-jre|-jdk|-headless|-jre-headless|-jdk-headless|-fpm|-cli)?$`)

// packageImage is the image a package corresponds to, and whether the package is one worth reporting.
func packageImage(name string) (string, bool) {
	if img, ok := knownPackages[name]; ok {
		return img, true
	}
	if m := versionedPackage.FindStringSubmatch(name); m != nil {
		return map[string]string{
			"postgresql": "docker.io/library/postgres", "openjdk": "docker.io/library/eclipse-temurin",
			"php": "docker.io/library/php", "tomcat": "docker.io/library/tomcat", "python": "docker.io/library/python",
		}[m[1]], true
	}
	return PackageRegistry + "/" + name, false
}

// serviceName is the name a package suggests for its service: the software, not the package.
func serviceName(pkg string) string {
	if m := versionedPackage.FindStringSubmatch(pkg); m != nil {
		return m[1]
	}
	switch pkg {
	case "nginx-core":
		return "nginx"
	case "redis-server":
		return "redis"
	case "mysql-server":
		return "mysql"
	case "mariadb-server":
		return "mariadb"
	case "mongodb-org-server":
		return "mongodb"
	case "libssl3", "openssl-libs":
		return "openssl"
	case "postgresql-server":
		return "postgresql"
	case "java-11-openjdk-headless", "java-17-openjdk-headless", "java-21-openjdk-headless":
		return "openjdk"
	case "containerd.io":
		return "containerd"
	}
	return pkg
}
