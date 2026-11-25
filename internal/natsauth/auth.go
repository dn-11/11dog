package natsauth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// SanitizeSubjectToken normalizes a node or task ID into a safe single NATS subject token.
func SanitizeSubjectToken(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// Manager holds the NATS Operator and Account signing keys and issues scoped User .creds JWTs.
type Manager struct {
	operatorSeed string
	operatorPub  string
	operatorJWT  string

	sysAccountPub string
	sysAccountJWT string

	accountKP   nkeys.KeyPair
	accountSeed string
	accountPub  string
	accountJWT  string

	streamName string
}

// NewManager initializes a NATS JWT/NKey authority. Empty seeds are freshly generated.
func NewManager(operatorSeed, accountSeed, streamName string) (*Manager, error) {
	if strings.TrimSpace(streamName) == "" {
		streamName = "DOG_TASKS"
	}

	var opKP nkeys.KeyPair
	var err error
	operatorSeed = strings.TrimSpace(operatorSeed)
	if operatorSeed == "" {
		opKP, err = nkeys.CreateOperator()
		if err != nil {
			return nil, fmt.Errorf("create operator keypair: %w", err)
		}
		seedBytes, err := opKP.Seed()
		if err != nil {
			return nil, fmt.Errorf("export operator seed: %w", err)
		}
		operatorSeed = string(seedBytes)
	} else {
		opKP, err = nkeys.FromSeed([]byte(operatorSeed))
		if err != nil {
			return nil, fmt.Errorf("parse operator seed: %w", err)
		}
	}
	opPub, err := opKP.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("operator public key: %w", err)
	}

	var accKP nkeys.KeyPair
	accountSeed = strings.TrimSpace(accountSeed)
	if accountSeed == "" {
		accKP, err = nkeys.CreateAccount()
		if err != nil {
			return nil, fmt.Errorf("create account keypair: %w", err)
		}
		seedBytes, err := accKP.Seed()
		if err != nil {
			return nil, fmt.Errorf("export account seed: %w", err)
		}
		accountSeed = string(seedBytes)
	} else {
		accKP, err = nkeys.FromSeed([]byte(accountSeed))
		if err != nil {
			return nil, fmt.Errorf("parse account seed: %w", err)
		}
	}
	accPub, err := accKP.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("account public key: %w", err)
	}

	sysKP, err := nkeys.CreateAccount()
	if err != nil {
		return nil, fmt.Errorf("create system account keypair: %w", err)
	}
	sysPub, err := sysKP.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("system account public key: %w", err)
	}
	sysClaims := jwt.NewAccountClaims(sysPub)
	sysClaims.Name = "SYS"
	sysJWT, err := sysClaims.Encode(opKP)
	if err != nil {
		return nil, fmt.Errorf("encode system account jwt: %w", err)
	}

	opClaims := jwt.NewOperatorClaims(opPub)
	opClaims.Name = "11dog-operator"
	opClaims.SystemAccount = sysPub
	opJWT, err := opClaims.Encode(opKP)
	if err != nil {
		return nil, fmt.Errorf("encode operator jwt: %w", err)
	}

	accClaims := jwt.NewAccountClaims(accPub)
	accClaims.Name = "11dog"
	accClaims.Limits.JetStreamLimits = jwt.JetStreamLimits{
		MemoryStorage:        jwt.NoLimit,
		DiskStorage:          jwt.NoLimit,
		Streams:              jwt.NoLimit,
		Consumer:             jwt.NoLimit,
		MemoryMaxStreamBytes: jwt.NoLimit,
		DiskMaxStreamBytes:   jwt.NoLimit,
		MaxAckPending:        jwt.NoLimit,
	}
	accJWT, err := accClaims.Encode(opKP)
	if err != nil {
		return nil, fmt.Errorf("encode account jwt: %w", err)
	}

	return &Manager{
		operatorSeed:  operatorSeed,
		operatorPub:   opPub,
		operatorJWT:   opJWT,
		sysAccountPub: sysPub,
		sysAccountJWT: sysJWT,
		accountKP:     accKP,
		accountSeed:   accountSeed,
		accountPub:    accPub,
		accountJWT:    accJWT,
		streamName:    streamName,
	}, nil
}

func (m *Manager) OperatorSeed() string { return m.operatorSeed }
func (m *Manager) OperatorPub() string  { return m.operatorPub }
func (m *Manager) OperatorJWT() string  { return m.operatorJWT }
func (m *Manager) AccountSeed() string  { return m.accountSeed }
func (m *Manager) AccountPub() string   { return m.accountPub }
func (m *Manager) AccountJWT() string   { return m.accountJWT }

// IssueServerCreds mints a full-privilege User JWT and NKey seed for 11dog-server inside the account.
func (m *Manager) IssueServerCreds() (userJWT string, userSeed string, creds string, err error) {
	uKP, err := nkeys.CreateUser()
	if err != nil {
		return "", "", "", fmt.Errorf("create server user keypair: %w", err)
	}
	uPub, err := uKP.PublicKey()
	if err != nil {
		return "", "", "", err
	}
	uSeedBytes, err := uKP.Seed()
	if err != nil {
		return "", "", "", err
	}

	uc := jwt.NewUserClaims(uPub)
	uc.Name = "11dog-server"
	uc.IssuerAccount = m.accountPub
	encoded, err := uc.Encode(m.accountKP)
	if err != nil {
		return "", "", "", fmt.Errorf("encode server user jwt: %w", err)
	}
	formatted, err := jwt.FormatUserConfig(encoded, uSeedBytes)
	if err != nil {
		return "", "", "", fmt.Errorf("format server creds: %w", err)
	}
	return encoded, string(uSeedBytes), string(formatted), nil
}

// IssueAgentCreds mints a strictly subject-scoped NATS .creds bundle for a single probe node.
func (m *Manager) IssueAgentCreds(nodeID string) (string, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return "", errors.New("nodeID is required")
	}
	safeID := SanitizeSubjectToken(nodeID)
	consumerName := "node_" + safeID

	uKP, err := nkeys.CreateUser()
	if err != nil {
		return "", fmt.Errorf("create agent user keypair: %w", err)
	}
	uPub, err := uKP.PublicKey()
	if err != nil {
		return "", err
	}
	uSeedBytes, err := uKP.Seed()
	if err != nil {
		return "", err
	}

	uc := jwt.NewUserClaims(uPub)
	uc.Name = nodeID
	uc.IssuerAccount = m.accountPub
	uc.Permissions = jwt.Permissions{
		Pub: jwt.Permission{
			Allow: jwt.StringList{
				"results.task.*",
				"agent.hb." + safeID,
				"agent.update." + safeID,
				"$JS.ACK." + m.streamName + "." + consumerName + ".>",
				"$JS.API.STREAM.INFO." + m.streamName,
				"$JS.API.CONSUMER.INFO." + m.streamName + "." + consumerName,
				"$JS.API.CONSUMER.CREATE." + m.streamName + "." + consumerName,
				"$JS.API.CONSUMER.CREATE." + m.streamName + "." + consumerName + ".>",
				"$JS.API.CONSUMER.MSG.NEXT." + m.streamName + "." + consumerName,
			},
		},
		Sub: jwt.Permission{
			Allow: jwt.StringList{
				"tasks.node." + safeID,
				"ctrl.node." + safeID + ".>",
				"_INBOX.>",
			},
		},
	}

	encoded, err := uc.Encode(m.accountKP)
	if err != nil {
		return "", fmt.Errorf("encode agent user jwt: %w", err)
	}
	formatted, err := jwt.FormatUserConfig(encoded, uSeedBytes)
	if err != nil {
		return "", fmt.Errorf("format agent creds: %w", err)
	}
	return string(formatted), nil
}

// ExtractNodeIDFromCreds parses a NATS .creds bundle and returns the authoritative node ID (UserClaims.Name).
func ExtractNodeIDFromCreds(creds []byte) (string, error) {
	userJWT, err := jwt.ParseDecoratedJWT(creds)
	if err != nil {
		return "", fmt.Errorf("parse creds jwt: %w", err)
	}
	claims, err := jwt.DecodeUserClaims(userJWT)
	if err != nil {
		return "", fmt.Errorf("decode user claims: %w", err)
	}
	if strings.TrimSpace(claims.Name) == "" {
		return "", errors.New("creds jwt has empty user name")
	}
	return strings.TrimSpace(claims.Name), nil
}

// RenderNATSServerConfig generates a ready-to-use nats-server.conf snippet using MEMORY resolver preload.
func (m *Manager) RenderNATSServerConfig(storeDir string) string {
	if strings.TrimSpace(storeDir) == "" {
		storeDir = "/data/jetstream"
	}
	return fmt.Sprintf(`# Generated by 11dog-server (NATS Decentralized JWT/NKey Auth)
# Place this in nats-server.conf and start with: nats-server -c nats-server.conf

port: 4222
http_port: 8222

jetstream {
  store_dir: %q
}

# Optional: enable WebSocket listener so agents can connect via wss:// behind Nginx/Ingress
# websocket {
#   port: 8443
#   no_tls: true
# }

operator: %s
system_account: %s

resolver: MEMORY
resolver_preload: {
  %s: %s
  %s: %s
}
`, storeDir, m.operatorJWT, m.sysAccountPub, m.sysAccountPub, m.sysAccountJWT, m.accountPub, m.accountJWT)
}
