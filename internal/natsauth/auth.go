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

// Manager holds the NATS Account signing key (provisioned externally, e.g. via Helm)
// and issues scoped User .creds JWTs for 11dog-server and edge probe agents.
type Manager struct {
	accountKP  nkeys.KeyPair
	accountPub string
	streamName string
}

// NewManager initializes a NATS User JWT signer from an externally provisioned Account NKey Seed (SA...).
func NewManager(accountSeed, streamName string) (*Manager, error) {
	accountSeed = strings.TrimSpace(accountSeed)
	if accountSeed == "" {
		return nil, errors.New("nats.account_seed (or DOG_NATS_ACCOUNT_SEED) is required")
	}
	if strings.TrimSpace(streamName) == "" {
		streamName = "DOG_TASKS"
	}

	accKP, err := nkeys.FromSeed([]byte(accountSeed))
	if err != nil {
		return nil, fmt.Errorf("parse nats account seed: %w", err)
	}
	accPub, err := accKP.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("derive nats account public key: %w", err)
	}
	if !strings.HasPrefix(accPub, "A") {
		return nil, fmt.Errorf("expected NATS Account seed (public key starting with A), got %q", accPub)
	}

	return &Manager{
		accountKP:  accKP,
		accountPub: accPub,
		streamName: streamName,
	}, nil
}

// AccountPub returns the NATS Account public key (A...).
func (m *Manager) AccountPub() string { return m.accountPub }

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
