package accounts

import (
	"encoding/json"
	"github.com/spacemeshos/go-spacemesh/crypto"
	"github.com/spacemeshos/go-spacemesh/filesystem"
	"github.com/spacemeshos/go-spacemesh/log"
	"io/ioutil"
	"path/filepath"
	"strings"
)

// Persisted node data
type AccountData struct {
	PublicKey  string          `json:"publicKey"`
	CryptoData CryptoData      `json:"crypto"`
	KDParams   crypto.KDParams `json:"kd"`
}

type CryptoData struct {
	Cipher     string `json:"cipher"`
	CipherText string `json:"cipherText"` // encrypted private key
	CipherIv   string `json:"cipherIv"`
	Mac        string `json:"mac"`
}

// Loads all accounts from store
func LoadAllAccounts() error {

	accountsDataFolder, err := filesystem.GetAccountsDataDirectoryPath()
	if err != nil {
		return err
	}

	files, err := ioutil.ReadDir(accountsDataFolder)
	if err != nil {
		log.Error("Failed to read account directory files", err)
		return nil
	}

	for _, f := range files {
		fileName := f.Name()
		if !f.IsDir() && strings.HasSuffix(fileName, ".json") {

			accountId := fileName[:strings.LastIndex(fileName, ".")]
			NewAccountFromStore(accountId, accountsDataFolder)
		}
	}

	return nil

}

// Creates a new account by id and stored data
// Account will be locked after creation as there's no persisted passphrase
// accountsDataPath: os-specific full path to accounts data folder
func NewAccountFromStore(accountId string, accountsDataPath string) (*Account, error) {

	log.Info("Loading account from store. Id: %s ...", accountId)

	fileName := accountId + ".json"
	dataFilePath := filepath.Join(accountsDataPath, fileName)

	data, err := ioutil.ReadFile(dataFilePath)
	if err != nil {
		log.Error("Failed to read node data from file", err)
		return nil, err
	}

	var accountData AccountData
	err = json.Unmarshal(data, &accountData)
	if err != nil {
		log.Error("Failed to unmarshal account data", err)
		return nil, err
	}

	pubKey, err := crypto.NewPublicKeyFromString(accountData.PublicKey)
	if err != nil {
		log.Error("Invalid account public key", err)
		return nil, err
	}

	acct := &Account{nil,
		pubKey,
		accountData.CryptoData,
		accountData.KDParams}

	log.Info("Loaded account from store: %s", pubKey.String())

	Accounts.All[acct.String()] = acct

	return acct, nil
}

// Persist all account data to store
// Passphrases are never persisted to store
// accountsDataPath: os-specific full path to accounts data folder
// Returns full path of persisted file (useful for testing)
func (a *Account) Persist(accountsDataPath string) (string, error) {

	pubKeyStr := a.PubKey.String()

	data := &AccountData{
		pubKeyStr,
		a.cryptoData,
		a.kdParams,
	}

	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Error("Failed to marshal node data to json", err)
		return "", err
	}

	fileName := a.String() + ".json"
	dataFilePath := filepath.Join(accountsDataPath, fileName)
	err = ioutil.WriteFile(dataFilePath, bytes, filesystem.OwnerReadWrite)
	if err != nil {
		log.Error("Failed to write account to file", err)
		return "", err
	}

	log.Info("Persisted account to store. Id: %s", a.String())

	return dataFilePath, nil
}
