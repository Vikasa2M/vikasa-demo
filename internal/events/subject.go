package events

import (
	"fmt"
	"regexp"
	"strings"
)

var tokenRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func Subject(dot, district, cabinet, service, controller, event string) (string, error) {
	toks := []string{dot, district, cabinet, service, controller, event}
	for _, tk := range toks {
		if !tokenRe.MatchString(tk) {
			return "", fmt.Errorf("invalid subject token %q", tk)
		}
	}
	return "vikasa." + strings.Join(toks, "."), nil
}

func ServiceEvent(ceType string) (string, string, error) {
	parts := strings.Split(ceType, ".")
	// vikasa.<service>.<event>.v<n>
	if len(parts) != 4 || parts[0] != "vikasa" || !strings.HasPrefix(parts[3], "v") {
		return "", "", fmt.Errorf("malformed ce-type %q", ceType)
	}
	return parts[1], parts[2], nil
}

type SubjectParts struct{ Dot, District, Cabinet, Service, Controller, Event string }

func ParseSubject(subj string) (SubjectParts, error) {
	p := strings.Split(subj, ".")
	if len(p) != 7 || p[0] != "vikasa" {
		return SubjectParts{}, fmt.Errorf("subject %q is not 7-token vikasa form", subj)
	}
	return SubjectParts{p[1], p[2], p[3], p[4], p[5], p[6]}, nil
}

// ParseShareSubject parses a DMZ federation subject:
// vikasa.<dot>.share.<corridor>.<cabinet>.<service>.<controller>.<event>
// (8 tokens). The DMZ subject transform replaces the 1-token district
// (e.g. "d1") with the 2-token "share.<corridor>" (e.g. "share.i85"), so the
// share form has one more token than ParseSubject's 7-token internal form.
// District is set to "share-<corridor>" (e.g. "share-i85") so the
// federation DB's district column stays a single meaningful
// LowCardinality-friendly value instead of splitting share/corridor apart.
func ParseShareSubject(subj string) (SubjectParts, error) {
	p := strings.Split(subj, ".")
	if len(p) != 8 || p[0] != "vikasa" || p[2] != "share" {
		return SubjectParts{}, fmt.Errorf("subject %q is not 8-token vikasa share form", subj)
	}
	return SubjectParts{
		Dot:        p[1],
		District:   "share-" + p[3],
		Cabinet:    p[4],
		Service:    p[5],
		Controller: p[6],
		Event:      p[7],
	}, nil
}
