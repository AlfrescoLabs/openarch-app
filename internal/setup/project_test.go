package setup

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

func TestParseAlfrescoProject(t *testing.T) {
	data := mustRead(t, "testdata/compose-installer-26.2.json")
	p, err := parseAlfrescoProject("/home/user/alfresco", data)
	if err != nil {
		t.Fatal(err)
	}
	want := AlfrescoProject{Dir: "/home/user/alfresco", Name: "stock", Repository: "alfresco", Broker: "activemq", Indexer: "batch-indexer",
		ContentApp: "content-app", RepositoryImage: "stock-alfresco", ACSVersion: "26.2.0", ACAVersion: "8.0.0", Search: "opensearch", BrokerUser: "admin"}
	got := p
	got.brokerPassword, got.javaOpts, got.networks, got.repositoryMounts = "", "", nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if p.brokerPassword != "test-secret" || !strings.Contains(p.javaOpts, "-Dmessaging.broker.url=") {
		t.Errorf("secrets: %q, JAVA_OPTS %q", p.brokerPassword, p.javaOpts)
	}
	if strings.Join(p.networks["alfresco"], ",") != "default" {
		t.Errorf("networks = %v", p.networks)
	}
}

func TestParseAlfrescoProjectErrors(t *testing.T) {
	for _, c := range []struct {
		name, config string
		code         output.ExitCode
		want         string
	}{
		{"no repository", `{"name":"p","services":{"web":{"image":"nginx"}}}`, output.ExitUsage, "has no Alfresco repository service"},
		{"two repositories", `{"name":"p","services":{"a":{"image":"alfresco/alfresco-content-repository-community:26.2.0"},"b":{"image":"alfresco/alfresco-content-repository-community:26.2.0"}}}`, output.ExitUsage, "more than one Alfresco repository: a, b"},
		{"no broker", `{"name":"p","services":{"alfresco":{"image":"alfresco/alfresco-content-repository-community:26.2.0"}}}`, output.ExitUsage, "has no ActiveMQ broker"},
		{"old", `{"name":"p","services":{"alfresco":{"image":"alfresco/alfresco-content-repository-community:23.4.0"},"mq":{"image":"alfresco/alfresco-activemq:5.18"}}}`, output.ExitPrerequisite, `version "23.4.0"`},
		{"not json", `nope`, output.ExitError, "cannot parse"},
	} {
		_, err := parseAlfrescoProject("/p", []byte(c.config))
		if output.CodeOf(err) != c.code || err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want code %d and %q", c.name, err, c.code, c.want)
		}
	}
	// The broker login can come from the repository JAVA_OPTS.
	p, err := parseAlfrescoProject("/p", []byte(`{"name":"p","services":{
		"repo":{"image":"alfresco/alfresco-content-repository-community:25.2.0","environment":{"JAVA_OPTS":"-Dmessaging.broker.username=mq -Dmessaging.broker.password=pw"}},
		"mq":{"image":"alfresco/alfresco-activemq:5.18"}}}`))
	if err != nil || p.BrokerUser != "mq" || p.brokerPassword != "pw" || p.RepositoryImage != "alfresco/alfresco-content-repository-community:25.2.0" {
		t.Errorf("login from JAVA_OPTS: %+v, %v", p, err)
	}
}

func TestInspectAlfrescoProject(t *testing.T) {
	data := mustRead(t, "testdata/compose-installer-26.2.json")
	var call string
	env := Env{Run: func(_ context.Context, dir string, _ io.Reader, name string, args ...string) ([]byte, error) {
		call = dir + ": " + name + " " + strings.Join(args, " ")
		return data, nil
	}}
	if p, err := InspectAlfrescoProject(context.Background(), env, "/home/user/alfresco"); err != nil || p.Repository != "alfresco" {
		t.Errorf("inspect: %+v, %v", p, err)
	}
	if call != "/home/user/alfresco: docker compose config --format json" {
		t.Errorf("call = %q", call)
	}
	env.Run = func(context.Context, string, io.Reader, string, ...string) ([]byte, error) {
		return nil, errors.New("no configuration file provided")
	}
	if _, err := InspectAlfrescoProject(context.Background(), env, "/nowhere"); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("no project: %v", err)
	}
}
