package tests

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/subnet-evm/accounts/abi/bind"
	"github.com/ava-labs/subnet-evm/core/types"
	"github.com/ava-labs/subnet-evm/ethclient"
	"github.com/ava-labs/subnet-evm/rpc"
	teleportermessenger "github.com/ava-labs/teleporter/abi-bindings/go/Teleporter/TeleporterMessenger"
	teleporterregistry "github.com/ava-labs/teleporter/abi-bindings/go/Teleporter/upgrades/TeleporterRegistry"
	"github.com/ava-labs/teleporter/tests/network"
	"github.com/ava-labs/teleporter/tests/utils"
	localUtils "github.com/ava-labs/teleporter/tests/utils/local-network-utils"
	deploymentUtils "github.com/ava-labs/teleporter/utils/deployment-utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	. "github.com/onsi/gomega"
)

// Tests basic one-way send from Subnet A to C Chain and vice versa
func BasicCChainSendReceive(network network.Network) {
	var (
		teleporterMessageID *big.Int
	)

	subnets := network.GetSubnetsInfo()
	Expect(len(subnets)).Should(BeNumerically(">=", 2))
	subnetAInfo := subnets[0]

	crpcconn, err := rpc.Dial(fmt.Sprintf("%s/ext/bc/C/rpc", subnets[1].ChainNodeURIs[0]))
	Expect(err).Should(BeNil())
	cethclient := ethclient.NewClient(crpcconn)

	cchainid, err := cethclient.ChainID(context.Background())
	Expect(err).Should(BeNil())

	fundedAddress, fundedKey := network.GetFundedAccountInfo()

	rawTeleporterDeployerTransaction, _, rawTeleporterContractAddress, err :=
		deploymentUtils.ConstructKeylessTransaction("./contracts/out/TeleporterMessenger.sol/TeleporterMessenger.json", false)
	Expect(err).Should(BeNil())

	err = crpcconn.CallContext(context.Background(), nil, "eth_sendRawTransaction", hexutil.Encode(rawTeleporterDeployerTransaction))
	Expect(err).Should(BeNil())
	time.Sleep(10 * time.Second)

	teleporterCode, err := cethclient.CodeAt(context.Background(), rawTeleporterContractAddress, nil)
	Expect(err).Should(BeNil())
	Expect(len(teleporterCode)).Should(BeNumerically(">", 2)) // 0x is an EOA, contract returns the bytecode

	teleporterMessenger, err := teleportermessenger.NewTeleporterMessenger(
		rawTeleporterContractAddress, cethclient,
	)
	Expect(err).Should(BeNil())

	opts, err := bind.NewKeyedTransactorWithChainID(fundedKey, cchainid)
	Expect(err).Should(BeNil())

	teleporterRegistryAddress, deployTx, _, err := teleporterregistry.DeployTeleporterRegistry(
		opts, cethclient, []teleporterregistry.ProtocolRegistryEntry{
			{
				Version:         big.NewInt(1),
				ProtocolAddress: rawTeleporterContractAddress,
			},
		},
	)
	Expect(err).Should(BeNil())

	deployReceipt, err := bind.WaitMined(context.Background(), cethclient, deployTx)
	Expect(err).Should(BeNil())
	Expect(deployReceipt.Status).Should(Equal(types.ReceiptStatusSuccessful))

	subnetBInfo := utils.SubnetTestInfo{
		SubnetID:                  ids.Empty,
		BlockchainID:              constants.EVMID,
		ChainNodeURIs:             subnets[1].ChainNodeURIs,
		ChainWSClient:             cethclient,
		ChainRPCClient:            cethclient,
		ChainIDInt:                cchainid,
		TeleporterRegistryAddress: teleporterRegistryAddress,
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

	receipt, teleporterMessageID := utils.SendCrossChainMessageAndWaitForAcceptance(
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

	//
	// Check Teleporter message received on the destination
	//
	delivered, err := subnetBInfo.TeleporterMessenger.MessageReceived(
		&bind.CallOpts{}, subnetAInfo.BlockchainID, teleporterMessageID,
	)
	Expect(err).Should(BeNil())
	Expect(delivered).Should(BeTrue())

	//
	// Send a transaction to Subnet B to issue a Warp Message from the Teleporter contract to Subnet A
	//
	sendCrossChainMessageInput.DestinationBlockchainID = subnetAInfo.BlockchainID
	sendCrossChainMessageInput.FeeInfo.Amount = big.NewInt(0)
	receipt, teleporterMessageID = utils.SendCrossChainMessageAndWaitForAcceptance(
		ctx,
		subnetBInfo,
		subnetAInfo,
		sendCrossChainMessageInput,
		fundedKey,
	)

	//
	// Relay the message to the destination
	//
	network.RelayMessage(ctx, receipt, subnetBInfo, subnetAInfo, true)

	//
	// Check Teleporter message received on the destination
	//
	delivered, err = subnetAInfo.TeleporterMessenger.MessageReceived(
		&bind.CallOpts{}, subnetBInfo.BlockchainID, teleporterMessageID,
	)
	Expect(err).Should(BeNil())
	Expect(delivered).Should(BeTrue())

	utils.RedeemRelayerRewardsAndConfirm(
		ctx, subnetAInfo, feeToken, feeTokenAddress, fundedKey, feeAmount,
	)
}
