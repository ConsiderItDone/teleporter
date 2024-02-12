package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/ava-labs/subnet-evm/accounts/abi/bind"
	"github.com/ava-labs/subnet-evm/core/types"
	"github.com/ava-labs/subnet-evm/ethclient"
	erc20bridge "github.com/ava-labs/teleporter/abi-bindings/go/CrossChainApplications/ERC20Bridge/ERC20Bridge"
	ics20bridge "github.com/ava-labs/teleporter/abi-bindings/go/CrossChainApplications/ERC20Bridge/ICS20Bridge"
	exampleerc20 "github.com/ava-labs/teleporter/abi-bindings/go/Mocks/ExampleERC20"
	teleportermessenger "github.com/ava-labs/teleporter/abi-bindings/go/Teleporter/TeleporterMessenger"
	teleporterregistry "github.com/ava-labs/teleporter/abi-bindings/go/Teleporter/upgrades/TeleporterRegistry"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	client  ethclient.Client
	pkey    *ecdsa.PrivateKey
	chainID *big.Int
	opts    *bind.TransactOpts
)

func init() {
	var err error

	rawPkey, existRawPkey := os.LookupEnv("PRIVATE_KEY")
	if !existRawPkey {
		panic("PRIVATE_KEY not found")
	}
	pkey, err = crypto.HexToECDSA(rawPkey)
	if err != nil {
		panic(fmt.Sprintf("bad private key value: %s", err))
	}

	ethRpcUrl, existEthRpcUrl := os.LookupEnv("ETH_RPC_URL")
	if !existEthRpcUrl {
		panic("ETH_RPC_URL not found")
	}
	client, err = ethclient.Dial(ethRpcUrl)
	if err != nil {
		panic(fmt.Sprintf("bad rpc value: %s", err))
	}

	chainID, err = client.ChainID(context.Background())
	if err != nil {
		panic(fmt.Sprintf("can't read chain ID: %s", err))
	}

	opts, err = bind.NewKeyedTransactorWithChainID(pkey, chainID)
	if err != nil {
		panic(fmt.Sprintf("can't create transactor: %s", err))
	}
}

func deployExampleERC20() (common.Address, error) {
	return deployer(func() (*types.Transaction, error) {
		_, tx, _, err := exampleerc20.DeployExampleERC20(opts, client)
		return tx, err
	})
}

func deployer(fn func() (*types.Transaction, error)) (common.Address, error) {
	tx, err := fn()
	if err != nil {
		return common.Address{}, err
	}
	time.Sleep(10 * time.Second)
	return bind.WaitDeployed(context.Background(), client, tx)
}

func print(msg string, addr common.Address, err error) {
	if err != nil {
		fmt.Printf("%s: %s\n", msg, err)
	} else {
		fmt.Printf("%s: %s\n", msg, addr)
	}
}

func printFn(msg string, fn func() (common.Address, error)) {
	addr, err := fn()
	print(msg, addr, err)
}

func main() {
	senderAddr := crypto.PubkeyToAddress(pkey.PublicKey)
	senderBalance, _ := client.BalanceAt(context.Background(), senderAddr, nil)
	fmt.Printf("Start deploy:\n")
	fmt.Printf("  ChainID: %s\n", chainID)
	fmt.Printf("  Sender:  %s\n", senderAddr)
	fmt.Printf("  Balance: %s\n", senderBalance)

	fmt.Printf("Deploy tokens [ExampleERC20]\n")
	printFn("  1", deployExampleERC20)
	printFn("  2", deployExampleERC20)
	printFn("  3", deployExampleERC20)

	fmt.Printf("Deploy [TeleporterMessenger]\n")
	teleporterMessenger, err := deployer(func() (*types.Transaction, error) {
		_, tx, _, err := teleportermessenger.DeployTeleporterMessenger(opts, client)
		return tx, err
	})
	print("  >", teleporterMessenger, err)

	fmt.Printf("Deploy [TeleporterRegistry]\n")
	teleporterRegistry, err := deployer(func() (*types.Transaction, error) {
		_, tx, _, err := teleporterregistry.DeployTeleporterRegistry(opts, client, []teleporterregistry.ProtocolRegistryEntry{
			{
				Version:         big.NewInt(1),
				ProtocolAddress: teleporterMessenger,
			},
		})
		return tx, err
	})
	print("  >", teleporterRegistry, err)

	fmt.Printf("Deploy [ERC20Bridge]\n")
	printFn("  >", func() (common.Address, error) {
		return deployer(func() (*types.Transaction, error) {
			_, tx, _, err := erc20bridge.DeployERC20Bridge(opts, client, teleporterRegistry)
			return tx, err
		})
	})

	fmt.Printf("Deploy [ICS20Bridge]\n")
	printFn("  >", func() (common.Address, error) {
		return deployer(func() (*types.Transaction, error) {
			_, tx, _, err := ics20bridge.DeployICS20Bridge(opts, client, teleporterRegistry, "some_channel")
			return tx, err
		})
	})
}
