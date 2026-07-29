package atrustruntime

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"

	clientpkg "github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/core"
	"inet.af/netaddr"
)

func TestSnapshotResourcesConvertsClientResources(t *testing.T) {
	client := resourceClientStub{
		ip: net.ParseIP("10.0.0.2"),
		ipResources: []clientpkg.IPResource{{
			IPMin: net.ParseIP("10.0.0.1"), IPMax: net.ParseIP("10.0.0.255"),
			PortMin: 443, PortMax: 443, Protocol: "tcp", AppID: "app-1", NodeGroupID: "node-1",
		}},
		domainResources: map[string]clientpkg.DomainResource{
			"example.edu": {PortMin: 1, PortMax: 65535, Protocol: "all", AppID: "app-2", NodeGroupID: "node-2"},
		},
		dnsRecords: map[string]net.IP{"app.example.edu": net.ParseIP("10.0.0.8")},
		dnsServer:  "10.0.0.53",
	}

	resources, err := snapshotResources(client)
	if err != nil {
		t.Fatalf("snapshotResources() error = %v", err)
	}
	want := core.Resources{
		ClientIP: "10.0.0.2",
		IPResources: []core.IPResource{{
			IPMin: "10.0.0.1", IPMax: "10.0.0.255", PortMin: 443, PortMax: 443,
			Protocol: "tcp", AppID: "app-1", NodeGroupID: "node-1",
		}},
		DomainResources: map[string]core.DomainResource{
			"example.edu": {PortMin: 1, PortMax: 65535, Protocol: "all", AppID: "app-2", NodeGroupID: "node-2"},
		},
		DNSRecords: map[string]string{"app.example.edu": "10.0.0.8"},
		DNSServer:  "10.0.0.53",
	}
	if !reflect.DeepEqual(resources, want) {
		t.Fatalf("resources = %#v, want %#v", resources, want)
	}
}

func TestSnapshotResourcesTreatsMissingResourcesAsEmpty(t *testing.T) {
	resources, err := snapshotResources(resourceClientStub{missing: true})
	if err != nil {
		t.Fatalf("snapshotResources() error = %v", err)
	}
	if resources.IPResources == nil || resources.DomainResources == nil || resources.DNSRecords == nil {
		t.Fatalf("missing resources are nil: %#v", resources)
	}
}

func TestSnapshotResourcesReturnsUnexpectedClientError(t *testing.T) {
	wantErr := errors.New("resource parser failed")
	_, err := snapshotResources(resourceClientStub{ipResourcesErr: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("snapshotResources() error = %v", err)
	}
}

type resourceClientStub struct {
	ip              net.IP
	ipResources     []clientpkg.IPResource
	domainResources map[string]clientpkg.DomainResource
	dnsRecords      map[string]net.IP
	dnsServer       string
	missing         bool
	ipResourcesErr  error
}

func (client resourceClientStub) IP() (net.IP, error) {
	if client.missing {
		return nil, clientpkg.ErrResourceNotFound
	}
	return client.ip, nil
}

func (client resourceClientStub) IPSet() (*netaddr.IPSet, error) {
	return nil, clientpkg.ErrResourceNotFound
}

func (client resourceClientStub) IPResources() ([]clientpkg.IPResource, error) {
	if client.ipResourcesErr != nil {
		return nil, client.ipResourcesErr
	}
	if client.missing {
		return nil, clientpkg.ErrResourceNotFound
	}
	return client.ipResources, nil
}

func (client resourceClientStub) DomainResources() (map[string]clientpkg.DomainResource, error) {
	if client.missing {
		return nil, clientpkg.ErrResourceNotFound
	}
	return client.domainResources, nil
}

func (client resourceClientStub) DNSResource() (map[string]net.IP, error) {
	if client.missing {
		return nil, clientpkg.ErrResourceNotFound
	}
	return client.dnsRecords, nil
}

func (client resourceClientStub) DNSServer() (string, error) {
	if client.missing {
		return "", clientpkg.ErrResourceNotFound
	}
	return client.dnsServer, nil
}

func (resourceClientStub) CanUseTCPTunnel() bool { return false }

func (resourceClientStub) DialTCP(context.Context, *net.TCPAddr) (net.Conn, error) {
	return nil, errors.New("not implemented")
}

func (resourceClientStub) NewL3Conn() (io.ReadWriteCloser, error) {
	return nil, errors.New("not implemented")
}
