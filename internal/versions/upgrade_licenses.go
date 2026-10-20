// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"

	"github.com/pipozzz/goliash/internal/store"
)

// LicenseChange is a package an upgrade brings under a more demanding license than the running version has it.
type LicenseChange struct {
	Name, Version, License string
	Class                  LicenseClass
	Was                    string // its license in the running version; "" when it is new there
}

// LicenseDelta is what an upgrade does to licenses, from the SBOMs of both images.
type LicenseDelta struct {
	Known   bool // both versions' main images have an SBOM Goliash read
	Changes []LicenseChange
}

// licenseNews are the classes an upgrade is worth stopping for: copyleft that reaches your code, source-available
// terms. Unknown licenses are too common to call out one by one.
func licenseNews(c LicenseClass) bool {
	return c == NetworkCopyleft || c == SourceAvailable || c == StrongCopyleft
}

// licenseIndex knows the application packages, with their licenses, of every running main image, by service,
// environment ("" anywhere) and version.
type licenseIndex map[vulnSide]map[string]LicensedPackage

func loadLicenseIndex(ctx context.Context, st *store.Store, sc store.Scope) (licenseIndex, error) {
	idx := licenseIndex{}
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return idx, err
	}
	sboms, err := st.ListImageSBOMs(ctx, sc)
	if err != nil {
		return idx, err
	}
	for _, in := range active {
		if !in.IsMain || in.Digest == "" {
			continue
		}
		b := sboms[ParseImage(in.Image).Repo()+"@"+in.Digest]
		if len(b.Purls) == 0 || NeedsLicenses(b) {
			continue
		}
		pkgs := map[string]LicensedPackage{}
		for _, p := range b.Purls {
			if osPackage(p) {
				continue
			}
			lic := b.Licenses[p]
			pkgs[purlPackage(p)] = LicensedPackage{Purl: p, Name: purlPackage(p), Version: purlVersion(p), License: lic, Class: ClassifyLicense(lic)}
		}
		for _, k := range []vulnSide{{in.ServiceID, in.EnvironmentID, in.Tag}, {in.ServiceID, "", in.Tag}} {
			if _, ok := idx[k]; !ok {
				idx[k] = pkgs
			}
		}
	}
	return idx, nil
}

// delta is what moving from the running side to the newer one does to licenses.
func (idx licenseIndex) delta(newer, running vulnSide) LicenseDelta {
	run, ok1 := idx[running]
	nw, ok2 := idx[newer]
	d := LicenseDelta{Known: ok1 && ok2}
	if !d.Known {
		return d
	}
	for name, p := range nw {
		if !licenseNews(p.Class) {
			continue
		}
		was, had := run[name]
		if had && was.Class.rank() >= p.Class.rank() {
			continue // as demanding before: nothing new to look at
		}
		c := LicenseChange{Name: name, Version: p.Version, License: p.License, Class: p.Class}
		if had {
			c.Was = was.License
		}
		d.Changes = append(d.Changes, c)
	}
	sort.Slice(d.Changes, func(i, j int) bool {
		a, b := d.Changes[i], d.Changes[j]
		if a.Class.rank() != b.Class.rank() {
			return a.Class.rank() > b.Class.rank()
		}
		return a.Name < b.Name
	})
	return d
}

// UpgradeLicenses is what moving a service in one environment from a version to another does to the licenses of
// its application packages (operating system packages left out): packages the newer version brings, or has
// relicensed, under network copyleft, source-available or strong copyleft terms. The running version is read in
// the environment, the newer one wherever it runs.
func UpgradeLicenses(ctx context.Context, st *store.Store, sc store.Scope, serviceID, envID, from, to string) (LicenseDelta, error) {
	idx, err := loadLicenseIndex(ctx, st, sc)
	if err != nil {
		return LicenseDelta{}, err
	}
	return idx.delta(vulnSide{serviceID, "", to}, vulnSide{serviceID, envID, from}), nil
}

// AddLicenseDeltas fills in each promotion's license delta, comparing the main images of the version waiting and
// the version running.
func AddLicenseDeltas(ctx context.Context, st *store.Store, sc store.Scope, list []Promotion) error {
	if len(list) == 0 {
		return nil
	}
	idx, err := loadLicenseIndex(ctx, st, sc)
	if err != nil {
		return err
	}
	for i := range list {
		p := &list[i]
		p.Licenses = idx.delta(vulnSide{p.Service.ID, p.From.ID, p.Version}, vulnSide{p.Service.ID, p.To.ID, p.Running})
	}
	return nil
}
