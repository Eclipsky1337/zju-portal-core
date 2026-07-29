package atrust

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/Eclipsky1337/zju-portal-core/client/atrust/auth"
	"github.com/Eclipsky1337/zju-portal-core/log"
)

type SetupConfig struct {
	ServerAddress           string
	ServerPort              int
	Username                string
	Password                string
	Phone                   string
	LoginDomain             string
	AuthType                string
	GraphCodeFile           string
	CASTicket               string
	OAuth2Code              string
	UpdateBestNodesInterval int
	BindInterface           string
	AutoDetectInterface     bool
	StageHandler            func(SetupStage)
	NodeSelectionHandler    func(map[string]string)
}

type SetupStage string

const (
	SetupStageDiscoveringAuth    SetupStage = "discovering_auth"
	SetupStageAuthenticating     SetupStage = "authenticating"
	SetupStageFetchingResources  SetupStage = "fetching_resources"
	SetupStageSelectingNodes     SetupStage = "selecting_nodes"
	SetupStageEstablishingTunnel SetupStage = "establishing_tunnel"
)

func (c *Client) SetupContext(ctx context.Context, config SetupConfig, authData, resourceData []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config.notify(SetupStageDiscoveringAuth)
	c.configureUnderlay(config)

	config.notify(SetupStageAuthenticating)
	authData, resourceData, err := c.authenticateAndFetchResources(ctx, config, authData, resourceData)
	if err != nil {
		return nil, err
	}
	if err := c.parseResource(resourceData); err != nil {
		return nil, err
	}

	log.DebugPrintf("SID: %s, DeviceID: %s, ConnectionID: %s, SignKey: %s", c.SID, c.DeviceID, c.ConnectionID, c.SignKey)
	config.notify(SetupStageSelectingNodes)
	if err := c.selectNodesAndAcquireIP(ctx); err != nil {
		return nil, err
	}
	config.notifyNodeSelection(c.bestNodes())
	config.notify(SetupStageEstablishingTunnel)
	if err := c.establishL3Tunnel(); err != nil {
		return nil, err
	}
	if config.UpdateBestNodesInterval > 0 {
		go c.updateBestNodes(c.lifecycleCtx, config.UpdateBestNodesInterval, config.NodeSelectionHandler)
	}
	return authData, nil
}

func (config SetupConfig) notify(stage SetupStage) {
	if config.StageHandler != nil {
		config.StageHandler(stage)
	}
}

func (config SetupConfig) notifyNodeSelection(nodes map[string]string) {
	if config.NodeSelectionHandler != nil {
		config.NodeSelectionHandler(nodes)
	}
}

func (c *Client) configureUnderlay(config SetupConfig) {
	c.serverAddress = config.ServerAddress
	serverHost := net.JoinHostPort(config.ServerAddress, fmt.Sprint(config.ServerPort))
	c.underlayDialer = newUnderlayDialer(serverHost, config.BindInterface, config.AutoDetectInterface)
	if interfaceName := c.underlayDialer.InterfaceName(); interfaceName != "" {
		log.Printf("Underlay interface: %s", interfaceName)
	} else if !config.AutoDetectInterface {
		log.Println("Underlay interface auto detection disabled; using system routing")
	} else {
		log.Println("Warning: failed to detect underlay interface; using system routing")
	}
}

func (c *Client) authenticateAndFetchResources(ctx context.Context, config SetupConfig, authData, resourceData []byte) ([]byte, []byte, error) {
	if c.SID != "" && c.DeviceID != "" && resourceData != nil {
		log.Println("Skipping login")
		c.ResumeStateReused = true
		c.ConnectionID = buildConnectionID(c.DeviceID)
		if c.SignKey == "" {
			c.SignKey = randHex(64)
		}
		config.notify(SetupStageFetchingResources)
		return authData, resourceData, nil
	}

	var clientAuthData auth.ClientAuthData
	if authData != nil {
		if err := json.Unmarshal(authData, &clientAuthData); err != nil {
			log.Println("Error parsing client data:", err)
			return nil, nil, err
		}
	}
	log.DebugPrintf("Given auth data: %+v", clientAuthData)
	if clientAuthData.DeviceID == "" {
		clientAuthData.DeviceID = strings.ToLower(randHex(32))
	}
	c.DeviceID = clientAuthData.DeviceID
	c.ConnectionID = buildConnectionID(c.DeviceID)
	c.SignKey = randHex(64)

	loginMethod, err := buildLoginMethod(config)
	if err != nil {
		return nil, nil, err
	}
	authServerHost := config.ServerAddress
	if config.ServerPort != 443 {
		authServerHost = fmt.Sprintf("%s:%d", config.ServerAddress, config.ServerPort)
	}
	session := auth.NewSession(authServerHost, c.underlayDialer.DialContext)
	session.SetAuthHandler(c.authHandler)
	loginResult, err := session.LoginContext(ctx, loginMethod, auth.LoginOptions{
		DeviceID: c.DeviceID,
		Cookies:  clientAuthData.Cookies,
	})
	if err != nil {
		log.Println("Login error:", err)
		return nil, nil, err
	}
	c.Username = loginResult.Username
	c.SID = loginResult.SID
	c.ResumeStateReused = loginResult.Reused
	clientAuthData.Cookies = loginResult.Cookies

	config.notify(SetupStageFetchingResources)
	resourceData, err = session.ClientResourceContext(ctx)
	if err != nil {
		log.Println("Error fetching client resource:", err)
		return nil, nil, err
	}
	authData, err = json.Marshal(clientAuthData)
	if err != nil {
		log.Println("Error marshaling auth data:", err)
	}
	return authData, resourceData, nil
}

func buildLoginMethod(config SetupConfig) (auth.LoginMethod, error) {
	switch config.AuthType {
	case "auth/psw":
		return auth.PasswordLogin{Username: config.Username, Password: config.Password, Domain: config.LoginDomain, GraphCodeFile: config.GraphCodeFile}, nil
	case "auth/cas":
		return auth.CASLogin{Domain: config.LoginDomain, Ticket: config.CASTicket}, nil
	case "auth/httpsOauth2":
		return auth.HTTPSOauth2Login{Domain: config.LoginDomain, Code: config.OAuth2Code}, nil
	case "auth/smsCheckCode":
		return auth.SMSLogin{Phone: config.Phone, Domain: config.LoginDomain, GraphCodeFile: config.GraphCodeFile}, nil
	case "":
		log.Println("No auth type specified, trying to skip auth")
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported auth type: %s", config.AuthType)
	}
}

func (c *Client) selectNodesAndAcquireIP(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.BestNodes = getBestNodes(ctx, c.NodeGroups, c.underlayDialer.DialContext)
	if err := c.getIP(ctx); err != nil {
		return err
	}
	c.underlayDialer.ExcludeIP(c.ip)
	return nil
}

func (c *Client) establishL3Tunnel() error {
	l3Tunnel, err := NewL3Tunnel(c)
	if err != nil {
		return fmt.Errorf("failed to create L3 tunnel: %w", err)
	}
	c.l3TunnelMu.Lock()
	c.l3Tunnel = l3Tunnel
	c.l3TunnelMu.Unlock()
	return nil
}
