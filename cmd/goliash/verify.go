// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// errVerifyFailed makes goliash verify exit 1: the image breaks a blocking rule.
var errVerifyFailed = errors.New("the image did not pass")

// verifyCmd checks an image before it is deployed, for CI: the server looks at its packages' vulnerabilities, its
// signature and its end of life. It exits 1 when a blocking rule is broken, 2 when the check could not run.
func verifyCmd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	server := fs.String("server", defaultServer(), "Goliash server URL (env GOLIASH_URL; default: the one signed in to)")
	token := fs.String("token", os.Getenv("GOLIASH_TOKEN"), "API token, glsh_api_… (env GOLIASH_TOKEN; default: goliash login's)")
	service := fs.String("service", "", "the service whose policy applies (default: the one running the image's repository)")
	sbomFile := fs.String("sbom", "", "the SPDX or CycloneDX JSON the build made (default: the image's attestation or package database)")
	failOn := fs.String("fail-on", "", "rules that fail the check, comma-separated: exploited, eol, signer, unsigned, critical, no-sbom, unreadable, or none (default exploited,eol,signer,unsigned)")
	asJSON := fs.Bool("json", false, "print the server's verdict as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: goliash verify [-service S] [-sbom FILE] [-fail-on RULES] IMAGE")
	}
	tok := tokenFor(*server, *token)
	if tok == nil {
		return errors.New("sign in first: goliash login " + *server + " (or set GOLIASH_TOKEN)")
	}
	body := map[string]any{"image": fs.Arg(0)}
	if *service != "" {
		body["service"] = *service
	}
	if *failOn != "" {
		var rules []string
		for _, r := range strings.Split(*failOn, ",") {
			if r = strings.TrimSpace(r); r != "" {
				rules = append(rules, r)
			}
		}
		body["fail_on"] = rules
	}
	if *sbomFile != "" {
		raw, err := os.ReadFile(*sbomFile)
		if err != nil {
			return err
		}
		if !json.Valid(raw) {
			return fmt.Errorf("%s is not JSON: hand in an SPDX or CycloneDX JSON document", *sbomFile)
		}
		body["sbom"] = json.RawMessage(raw)
	}
	t, err := tok(ctx)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(*server, "/")+"/api/v1/check", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		var p struct{ Title, Detail string }
		_ = json.Unmarshal(raw, &p)
		return fmt.Errorf("the server answered %d: %s %s", resp.StatusCode, p.Title, p.Detail)
	}
	var v struct {
		Image, Digest, Service string
		Passed                 bool
		Findings               []struct {
			Rule, Detail string
			Blocking     bool
		}
		Packages   int
		SBOMSource string `json:"sbom_source"`
		Signed     bool
		Signers    []string
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if *asJSON {
		_, _ = out.Write(raw)
	} else {
		verdict := "passed"
		if !v.Passed {
			verdict = "blocked"
		}
		where := v.Image
		if v.Digest != "" {
			where += " (" + v.Digest + ")"
		}
		_, _ = fmt.Fprintf(out, "%s: %s\n", where, verdict)
		for _, f := range v.Findings {
			mark := "  warn  "
			if f.Blocking {
				mark = "  BLOCK "
			}
			_, _ = fmt.Fprintf(out, "%s%s: %s\n", mark, f.Rule, f.Detail)
		}
		signed := "unsigned"
		if v.Signed {
			signed = "signed"
			if len(v.Signers) > 0 {
				signed += " by " + strings.Join(v.Signers, ", ")
			}
		}
		svc := ""
		if v.Service != "" {
			svc = "; policy of " + v.Service
		}
		_, _ = fmt.Fprintf(out, "  %d packages (SBOM: %s), %s%s\n", v.Packages, v.SBOMSource, signed, svc)
	}
	if !v.Passed {
		return errVerifyFailed
	}
	return nil
}
