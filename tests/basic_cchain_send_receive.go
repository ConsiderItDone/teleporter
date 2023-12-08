package tests

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/subnet-evm/accounts/abi/bind"
	"github.com/ava-labs/subnet-evm/core/types"
	"github.com/ava-labs/subnet-evm/ethclient"
	"github.com/ava-labs/subnet-evm/rpc"
	"github.com/ethereum/go-ethereum/common"
	. "github.com/onsi/gomega"

	teleportermessenger "github.com/ava-labs/teleporter/abi-bindings/go/Teleporter/TeleporterMessenger"
	teleporterregistry "github.com/ava-labs/teleporter/abi-bindings/go/Teleporter/upgrades/TeleporterRegistry"
	"github.com/ava-labs/teleporter/tests/network"
	"github.com/ava-labs/teleporter/tests/utils"
	localUtils "github.com/ava-labs/teleporter/tests/utils/local-network-utils"
)

// Tests basic one-way send from Subnet A to C Chain and vice versa
func BasicCChainSendReceive(network network.Network) {
	var (
	//teleporterMessageID *big.Int
	)

	subnets := network.GetSubnetsInfo()
	Expect(len(subnets)).Should(BeNumerically(">=", 2))
	subnetAInfo := subnets[0]

	crpcconn, err := rpc.Dial(fmt.Sprintf("%s/ext/bc/C/rpc", subnets[0].ChainNodeURIs[0]))
	Expect(err).Should(BeNil())
	cethclient := ethclient.NewClient(crpcconn)

	cchainid, err := cethclient.ChainID(context.Background())
	Expect(err).Should(BeNil())

	fundedAddress, fundedKey := network.GetFundedAccountInfo()

	opts, err := bind.NewKeyedTransactorWithChainID(fundedKey, cchainid)
	Expect(err).Should(BeNil())

	teleporterMessengerAddr, deployTeleporterMessengerTx, teleporterMessenger, err := teleportermessenger.DeployTeleporterMessenger(opts, cethclient)
	Expect(err).Should(BeNil())

	deployTeleporterMessengerReceipt, err := bind.WaitMined(context.Background(), cethclient, deployTeleporterMessengerTx)
	Expect(err).Should(BeNil())
	Expect(deployTeleporterMessengerReceipt.Status).Should(Equal(types.ReceiptStatusSuccessful))

	teleporterRegistryAddress, deployTeleporterRegistrTx, _, err := teleporterregistry.DeployTeleporterRegistry(
		opts, cethclient, []teleporterregistry.ProtocolRegistryEntry{
			{
				Version:         big.NewInt(1),
				ProtocolAddress: teleporterMessengerAddr,
			},
		},
	)
	Expect(err).Should(BeNil())

	deployTeleporterRegistryReceipt, err := bind.WaitMined(context.Background(), cethclient, deployTeleporterRegistrTx)
	Expect(err).Should(BeNil())
	Expect(deployTeleporterRegistryReceipt.Status).Should(Equal(types.ReceiptStatusSuccessful))

	subnetBInfo := utils.SubnetTestInfo{
		SubnetID:                  ids.Empty,
		BlockchainID:              constants.EVMID,
		ChainNodeURIs:             subnets[0].ChainNodeURIs,
		ChainWSClient:             cethclient,
		ChainRPCClient:            cethclient,
		ChainIDInt:                cchainid,
		TeleporterRegistryAddress: teleporterRegistryAddress, //teleporterRegistryAddress
		TeleporterMessenger:       teleporterMessenger,
	}

	teleporterContractAddress := network.GetTeleporterContractAddress()

	//
	// Send a transaction to Subnet A to issue a Warp Message from the Teleporter contract to Subnet B
	//
	ctx := context.Background()

	feeAmount := big.NewInt(1)
	feeTokenAddress, feeToken := localUtils.DeployExampleERC20(
		ctx,
		fundedKey,
		subnetAInfo,
	)
	localUtils.ExampleERC20Approve(
		ctx,
		feeToken,
		teleporterContractAddress,
		big.NewInt(0).Mul(big.NewInt(1e18),
			big.NewInt(10)),
		subnetAInfo,
		fundedKey,
	)

	sendCrossChainMessageInput := teleportermessenger.TeleporterMessageInput{
		DestinationBlockchainID: subnetBInfo.BlockchainID,
		DestinationAddress:      fundedAddress,
		FeeInfo: teleportermessenger.TeleporterFeeInfo{
			FeeTokenAddress: feeTokenAddress,
			Amount:          feeAmount,
		},
		RequiredGasLimit:        big.NewInt(1),
		AllowedRelayerAddresses: []common.Address{},
		Message:                 []byte{1, 2, 3, 4},
	}

	receipt, _ := utils.SendCrossChainMessageAndWaitForAcceptance(
		ctx,
		subnetAInfo,
		subnetBInfo,
		sendCrossChainMessageInput,
		fundedKey,
	)

	//
	// Relay the message to the destination
	//
	network.RelayMessage(ctx, receipt, subnetAInfo, subnetBInfo, true)

}
