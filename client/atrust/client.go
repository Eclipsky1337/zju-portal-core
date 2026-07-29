package atrust

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/client"
	"github.com/Eclipsky1337/zju-portal-core/client/atrust/auth"
	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/internal/underlay"
	"github.com/Eclipsky1337/zju-portal-core/log"
)

type Client struct {
	Username          string
	SID               string
	DeviceID          string
	ConnectionID      string
	SignKey           string
	ResumeStateReused bool

	serverAddress   string
	ipResources     []client.IPResource
	domainResources map[string]client.DomainResource
	dnsResource     map[string]net.IP
	dnsServer       string

	MajorNodeGroup   string
	NodeGroups       map[string][]string
	BestNodes        map[string]string
	BestNodesRWMutex sync.RWMutex

	ip net.IP // Client IP

	l3Tunnel   *L3Tunnel
	l3TunnelMu sync.Mutex

	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	lifecycleStop   func() bool
	closeOnce       sync.Once
	underlayDialer  *underlay.Dialer

	skipTCPTunnelWait bool
	authHandler       core.AuthHandler
}

func (c *Client) SetSkipTCPTunnelWait(skip bool) {
	c.skipTCPTunnelWait = skip
}

func (c *Client) SetAuthHandler(handler core.AuthHandler) {
	c.authHandler = handler
}

func NewClient(username, sid, deviceID, signKey string) *Client {
	return NewClientContext(context.Background(), username, sid, deviceID, signKey)
}

func NewClientContext(ctx context.Context, username, sid, deviceID, signKey string) *Client {
	lifecycleCtx, lifecycleCancel := context.WithCancel(ctx)
	client := &Client{
		Username:        username,
		SID:             sid,
		DeviceID:        deviceID,
		SignKey:         signKey,
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
	}
	client.lifecycleStop = context.AfterFunc(lifecycleCtx, client.Close)
	return client
}

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.lifecycleStop()
		c.lifecycleCancel()
		c.l3TunnelMu.Lock()
		tunnel := c.l3Tunnel
		c.l3TunnelMu.Unlock()
		if tunnel != nil {
			tunnel.Close()
		}
	})
}

func (c *Client) IP() (net.IP, error) {
	if c.ip == nil {
		return nil, errors.New("IP not available")
	}

	return c.ip.To4(), nil
}

func (c *Client) IPResources() ([]client.IPResource, error) {
	if c.ipResources == nil {
		return nil, errors.New("IP resources not available")
	}

	return c.ipResources, nil
}

func (c *Client) RouteExcludedIPs() []net.IP {
	var excluded []net.IP
	for _, addresses := range c.NodeGroups {
		for _, address := range addresses {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				continue
			}
			if ip := net.ParseIP(host).To4(); ip != nil {
				excluded = append(excluded, append(net.IP(nil), ip...))
			}
		}
	}
	return excluded
}

func (c *Client) DomainResources() (map[string]client.DomainResource, error) {
	if c.domainResources == nil {
		return nil, errors.New("domain resources not available")
	}

	return c.domainResources, nil
}

func (c *Client) DNSResource() (map[string]net.IP, error) {
	if c.dnsResource == nil {
		return nil, errors.New("DNS resource not available")
	}

	return c.dnsResource, nil
}

func (c *Client) DNSServer() (string, error) {
	if c.dnsServer == "" {
		return "", errors.New("DNS server not available")
	}

	return c.dnsServer, nil
}

func randHex(n int) string {
	numBytes := (n + 1) / 2
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToUpper(hex.EncodeToString(b)[:n])
}

func GetAuthInfoList(serverAddress string, serverPort int, bindInterface string, autoDetectInterface bool) ([]auth.AuthInfo, error) {
	var serverHost string
	if serverPort == 443 {
		serverHost = serverAddress
	} else {
		serverHost = fmt.Sprintf("%s:%d", serverAddress, serverPort)
	}
	dialer := newUnderlayDialer(serverHost, bindInterface, autoDetectInterface)
	sess := auth.NewSession(serverHost, dialer.DialContext)
	return sess.GetAuthInfoList()
}

func (c *Client) CanUseTCPTunnel() bool {
	return true
}

func (c *Client) NewL3Conn() (io.ReadWriteCloser, error) {
	c.l3TunnelMu.Lock()
	tunnel := c.l3Tunnel
	c.l3TunnelMu.Unlock()
	if tunnel == nil {
		return nil, errors.New("L3 tunnel not initialized")
	}
	return tunnel.NewL3Conn()
}

func SetTrusted(serverAddress string, serverPort int, authData []byte, trusted bool, bindInterface string, autoDetectInterface bool) error {
	var clientAuthData auth.ClientAuthData
	if authData != nil {
		err := json.Unmarshal(authData, &clientAuthData)
		if err != nil {
			log.Println("Error parsing client data:", err)
			return err
		}
	}
	log.DebugPrintf("Given auth data: %+v", clientAuthData)

	if clientAuthData.DeviceID == "" {
		clientAuthData.DeviceID = strings.ToLower(randHex(32))
	}

	var serverHost string
	if serverPort == 443 {
		serverHost = serverAddress
	} else {
		serverHost = fmt.Sprintf("%s:%d", serverAddress, serverPort)
	}
	dialer := newUnderlayDialer(serverHost, bindInterface, autoDetectInterface)
	sess := auth.NewSession(serverHost, dialer.DialContext)

	sess.Login(nil, auth.LoginOptions{
		DeviceID: clientAuthData.DeviceID,
		Cookies:  clientAuthData.Cookies,
	})
	result, err := sess.QueryDevice()
	if err != nil {
		return err
	}

	if trusted {
		if result.DeviceTrusted {
			log.Println("Device already trusted, skipping")
			return nil
		}
		return sess.TrustDevice([]string{result.SelfID})
	} else {
		if !result.DeviceTrusted {
			log.Println("Device already untrusted, skipping")
			return nil
		}
		return sess.UntrustDevice([]string{result.SelfID})
	}
}

func (c *Client) Setup(serverAddress string, serverPort int, username, password, phone, loginDomain, authType, graphCodeFile, casTicket, oauth2Code string, authData, resourceData []byte, updateBestNodesInterval int, bindInterface string, autoDetectInterface bool) ([]byte, error) {
	return c.SetupContext(c.lifecycleCtx, SetupConfig{
		ServerAddress:           serverAddress,
		ServerPort:              serverPort,
		Username:                username,
		Password:                password,
		Phone:                   phone,
		LoginDomain:             loginDomain,
		AuthType:                authType,
		GraphCodeFile:           graphCodeFile,
		CASTicket:               casTicket,
		OAuth2Code:              oauth2Code,
		UpdateBestNodesInterval: updateBestNodesInterval,
		BindInterface:           bindInterface,
		AutoDetectInterface:     autoDetectInterface,
	}, authData, resourceData)
}

func newUnderlayDialer(serverHost, bindInterface string, autoDetectInterface bool) *underlay.Dialer {
	return underlay.New(serverHost, underlay.Options{
		InterfaceName: bindInterface,
		AutoDetect:    autoDetectInterface,
	})
}

func buildConnectionID(deviceID string) string {
	sum := md5.Sum([]byte(deviceID))
	return fmt.Sprintf("%X-%d", sum, time.Now().UnixMicro())
}
