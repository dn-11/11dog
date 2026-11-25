package natsauth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

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

// IssueAgentCreds mints a strictly subject-scoped NATS .creds bundle for a single probe node UUID.
func (m *Manager) IssueAgentCreds(nodeID uuid.UUID) (string, error) {
	if nodeID == uuid.Nil {
		return "", errors.New("nodeID is required")
	}
	idStr := nodeID.String()
	consumerName := "node_" + idStr

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
	uc.Name = idStr
	uc.IssuerAccount = m.accountPub
	uc.Permissions = jwt.Permissions{
		Pub: jwt.Permission{
			Allow: jwt.StringList{
				"results.task.*",
				"agent.hb." + idStr,
				"agent.update." + idStr,
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
				"tasks.node." + idStr,
				"ctrl.node." + idStr + ".>",
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

// ExtractNodeIDFromCreds parses a NATS .creds bundle and returns the authoritative node UUID (UserClaims.Name).
func ExtractNodeIDFromCreds(creds []byte) (uuid.UUID, error) {
	userJWT, err := jwt.ParseDecoratedJWT(creds)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse creds jwt: %w", err)
	}
	claims, err := jwt.DecodeUserClaims(userJWT)
	if err != nil {
		return uuid.Nil, fmt.Errorf("decode user claims: %w", err)
	}
	nodeID, err := uuid.Parse(strings.TrimSpace(claims.Name))
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse creds node uuid %q: %w", claims.Name, err)
	}
	return nodeID, nil
}
