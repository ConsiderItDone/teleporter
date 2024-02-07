package flows

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/vms/platformvm"
	"github.com/ava-labs/subnet-evm/accounts/abi/bind"
	"github.com/ava-labs/subnet-evm/core/types"
	"github.com/ava-labs/subnet-evm/ethclient"
	"github.com/ava-labs/subnet-evm/rpc"
	teleportermessenger "github.com/ava-labs/teleporter/abi-bindings/go/Teleporter/TeleporterMessenger"
	teleporterregistry "github.com/ava-labs/teleporter/abi-bindings/go/Teleporter/upgrades/TeleporterRegistry"
	"github.com/ava-labs/teleporter/tests/interfaces"
	"github.com/ava-labs/teleporter/tests/utils"
	"github.com/davecgh/go-spew/spew"
	"github.com/ethereum/go-ethereum/log"
	. "github.com/onsi/gomega"
)

func ERC20BridgeMultihopIBC(network interfaces.Network) {
	cchainInfo, subnetInfo, _ := utils.GetThreeSubnets(network)
	teleporterContractAddress := network.GetTeleporterContractAddress()
	fundedAddress, fundedKey := network.GetFundedAccountInfo()
	ctx := context.Background()

	allblockchains, err := platformvm.NewClient(cchainInfo.NodeURIs[0]).GetBlockchains(context.Background())
	Expect(err).Should(BeNil())
	var (
		cchainDataInfo      platformvm.APIBlockchain
		cchainDataInfoFound bool
	)
	for _, bcInfo := range allblockchains {
		if bcInfo.Name == "C-Chain" {
			cchainDataInfo = bcInfo
			cchainDataInfoFound = true
		}
	}
	Expect(cchainDataInfoFound).Should(BeTrue())

	crpcconn1, err := rpc.Dial(fmt.Sprintf("%s/ext/bc/C/rpc", cchainInfo.NodeURIs[0]))
	Expect(err).Should(BeNil())

	crpcconn2, err := rpc.Dial(fmt.Sprintf("ws://%s/ext/bc/C/ws", strings.TrimPrefix(cchainInfo.NodeURIs[0], "http://")))
	Expect(err).Should(BeNil())

	cethclient := ethclient.NewClient(crpcconn1)
	wethclient := ethclient.NewClient(crpcconn2)

	cchainid, err := cethclient.ChainID(ctx)
	Expect(err).Should(BeNil())

	cchainTeleporterMessenger, err := teleportermessenger.NewTeleporterMessenger(
		teleporterContractAddress,
		cethclient,
	)
	Expect(err).Should(BeNil())
	subnetTeleporterMessenger, err := teleportermessenger.NewTeleporterMessenger(
		teleporterContractAddress,
		subnetInfo.RPCClient,
	)
	Expect(err).Should(BeNil())

	opts, err := bind.NewKeyedTransactorWithChainID(fundedKey, cchainid)
	Expect(err).Should(BeNil())
	teleporterRegistryAddress, tx, _, err := teleporterregistry.DeployTeleporterRegistry(
		opts, cethclient, []teleporterregistry.ProtocolRegistryEntry{
			{
				Version:         big.NewInt(1),
				ProtocolAddress: teleporterContractAddress,
			},
		},
	)
	Expect(err).Should(BeNil())
	receipt, err := bind.WaitMined(ctx, cethclient, tx)
	Expect(err).Should(BeNil())
	Expect(receipt.Status).Should(Equal(types.ReceiptStatusSuccessful))
	log.Info("Deployed TeleporterRegistry contract to subnet", ids.Empty.Hex(),
		"Deploy address", teleporterRegistryAddress.Hex())

	cchainInfo = interfaces.SubnetTestInfo{
		SubnetID:                  cchainDataInfo.SubnetID,
		BlockchainID:              cchainDataInfo.ID,
		NodeURIs:                  cchainInfo.NodeURIs,
		WSClient:                  wethclient,
		RPCClient:                 cethclient,
		EVMChainID:                cchainid,
		TeleporterMessenger:       cchainTeleporterMessenger,
		TeleporterRegistryAddress: teleporterRegistryAddress,
	}

	// Deploy an ERC20 to c chain
	nativeERC20Address, nativeERC20 := utils.DeployExampleERC20(
		context.Background(),
		fundedKey,
		cchainInfo,
	)

	// Deploy the ERC20 bridge to subnet A
	cchainErc20BridgeAddr, cchainErc20Bridge := utils.DeployERC20Bridge(ctx, fundedKey, cchainInfo)

	// Deploy the ICS20 bridge to subnet B
	subnetICS20BridgeAddr, subnetICS20Bridge := utils.DeployICS20Bridge(ctx, fundedKey, subnetInfo, "some_channel")
	_ = subnetICS20Bridge

	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(10000000000000))
	utils.ERC20Approve(
		ctx,
		nativeERC20,
		cchainErc20BridgeAddr,
		amount,
		cchainInfo,
		fundedKey,
	)

	// Send a transaction on c chain to add support for the the ERC20 token to the bridge on c
	receipt, messageID := submitCreateBridgeToken(
		ctx,
		cchainInfo,
		subnetInfo.BlockchainID,
		subnetICS20BridgeAddr,
		nativeERC20Address,
		nativeERC20Address,
		big.NewInt(0),
		fundedAddress,
		fundedKey,
		cchainErc20Bridge,
		cchainTeleporterMessenger,
	)

	// Relay message
	network.RelayMessage(ctx, receipt, cchainInfo, subnetInfo, true)

	// Check Teleporter message received on the destination
	delivered, err := subnetTeleporterMessenger.MessageReceived(
		&bind.CallOpts{},
		cchainInfo.BlockchainID,
		messageID,
	)
	Expect(err).Should(BeNil())
	Expect(delivered).Should(BeTrue())

	// Send a bridge transfer for the newly added token from subnet A to subnet B
	totalAmount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(13))
	primaryFeeAmount := big.NewInt(1e18)
	receipt, messageID = bridgeToken(
		ctx,
		cchainInfo,
		subnetInfo.BlockchainID,
		subnetICS20BridgeAddr,
		nativeERC20Address,
		fundedAddress,
		totalAmount,
		primaryFeeAmount,
		big.NewInt(0),
		fundedAddress,
		fundedKey,
		cchainErc20Bridge,
		true,
		cchainInfo.BlockchainID,
		cchainTeleporterMessenger,
	)

	// Relay message
	deliveryReceipt := network.RelayMessage(ctx, receipt, cchainInfo, subnetInfo, true)
	spew.Dump(deliveryReceipt)

	receiveEvent, err := utils.GetEventFromLogs(
		deliveryReceipt.Logs,
		subnetInfo.TeleporterMessenger.ParseReceiveCrossChainMessage)
	Expect(err).Should(BeNil())
	Expect(receiveEvent).ShouldNot(BeNil())

	execSuccessEvent, err := utils.GetEventFromLogs(deliveryReceipt.Logs, subnetInfo.TeleporterMessenger.ParseMessageExecuted)
	Expect(err).Should(HaveOccurred())
	Expect(execSuccessEvent).Should(BeNil())

	execFailedEvent, err := utils.GetEventFromLogs(deliveryReceipt.Logs, subnetInfo.TeleporterMessenger.ParseMessageExecutionFailed)
	Expect(err).Should(BeNil())
	Expect(execFailedEvent).ShouldNot(BeNil())
	spew.Dump(execFailedEvent)

	// Check Teleporter message received on the destination
	delivered, err = subnetTeleporterMessenger.MessageReceived(&bind.CallOpts{}, cchainInfo.BlockchainID, messageID)
	Expect(err).Should(BeNil())
	Expect(delivered).Should(BeTrue())
}
