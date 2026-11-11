package stack

import (
	"fmt"
	"strings"
	"testing"
)

func TestSources(t *testing.T) {
	for _, c := range []struct {
		s       Sources
		profile string
		extras  string
		name    string
	}{
		{Sources{Alfresco: 1}, "alfresco", "", "1 Alfresco"},
		{Sources{Alfresco: 2}, "alfresco", "alfresco-2", "2 Alfresco"},
		{Sources{Nuxeo: 1}, "nuxeo", "", "1 Nuxeo"},
		{Sources{Alfresco: 1, Nuxeo: 1}, "full", "", "1 Alfresco, 1 Nuxeo"},
		{Sources{Alfresco: 2, Nuxeo: 1}, "full", "alfresco-2", "2 Alfresco, 1 Nuxeo"},
	} {
		if err := c.s.Valid(); err != nil {
			t.Errorf("%v: %v", c.s, err)
		}
		if c.s.Profile() != c.profile || strings.Join(c.s.Extras(), ",") != c.extras || c.s.String() != c.name {
			t.Errorf("%v: %s %v %q", c.s, c.s.Profile(), c.s.Extras(), c.s.String())
		}
		// The deprecated flags and version 1 files give the same sources back.
		if back, err := SourcesOf(c.s.Profile(), c.s.Extras()); err != nil || back != c.s {
			t.Errorf("SourcesOf(%s, %v) = %v, %v", c.s.Profile(), c.s.Extras(), back, err)
		}
	}
	for _, bad := range []Sources{{}, {Alfresco: 3}, {Nuxeo: 2}, {Alfresco: -1, Nuxeo: 1}} {
		if bad.Valid() == nil {
			t.Errorf("%+v is valid", bad)
		}
	}
	if _, err := SourcesOf("nuxeo", []string{"alfresco-2"}); err == nil {
		t.Error("nuxeo with alfresco-2 is accepted")
	}
}

func TestHostPorts(t *testing.T) {
	for _, c := range []struct {
		profile              string
		extras               []string
		sampleUI, filesystem bool
		want                 string
	}{
		{"alfresco", nil, false, false, "[80 443 8090]"},
		{"full", []string{"alfresco-2"}, false, false, "[80 443 8090 8081 8082 8445]"},
		{"alfresco", nil, true, false, "[80 443 8090 4200]"},
		{"nuxeo", nil, false, true, "[80 443 8090 8081 9096]"},
	} {
		if got := fmt.Sprint(HostPorts(c.profile, c.extras, c.sampleUI, c.filesystem)); got != c.want {
			t.Errorf("HostPorts(%s, %v, %v, %v) = %s, want %s", c.profile, c.extras, c.sampleUI, c.filesystem, got, c.want)
		}
	}
}
