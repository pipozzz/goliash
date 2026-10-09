// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package host collects a Linux server: its operating system and the installed packages that matter, read
// from /etc/os-release and the dpkg or apk databases. It only reads files; it runs nothing on the host.
package host

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/collectors"
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
}

// New is the collectors.Factory for host targets.
func New(_ context.Context, t agentproto.Target) (collectors.Collector, error) {
	c := &Collector{root: "/", extra: map[string]bool{}}
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
func (c *Collector) Collect(context.Context) (collectors.Result, error) {
	osr, err := readKeyValues(filepath.Join(c.root, "etc", "os-release"))
	if err != nil {
		return collectors.Result{}, err
	}
	res := collectors.Result{Complete: true}
	id, version := osr["ID"], osr["VERSION_ID"]
	if id != "" && version != "" {
		res.Workloads = append(res.Workloads, workload("os/"+id, agentproto.HostOs, id, osImage(id)+":"+version))
	}
	pkgs, err := c.packages()
	if err != nil {
		res.Complete = false
		res.Errors = append(res.Errors, err.Error())
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

func workload(id string, kind agentproto.WorkloadKind, name, image string) agentproto.Workload {
	running := 1
	return agentproto.Workload{
		ID: id, Kind: kind, Name: name, DesiredReplicas: &running,
		Containers: []agentproto.Container{{Name: name, Image: image, Running: 1}},
	}
}

// packages reads the installed packages and their versions from dpkg or apk.
func (c *Collector) packages() (map[string]string, error) {
	if f, err := os.Open(filepath.Join(c.root, "var", "lib", "dpkg", "status")); err == nil {
		defer func() { _ = f.Close() }()
		return parseDpkg(f)
	}
	if f, err := os.Open(filepath.Join(c.root, "lib", "apk", "db", "installed")); err == nil {
		defer func() { _ = f.Close() }()
		return parseApk(f)
	}
	return nil, errors.New("no dpkg or apk package database found; packages are not reported (rpm is not supported yet)")
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
	f, err := os.Open(path)
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
	case "libssl3":
		return "openssl"
	case "containerd.io":
		return "containerd"
	}
	return pkg
}
