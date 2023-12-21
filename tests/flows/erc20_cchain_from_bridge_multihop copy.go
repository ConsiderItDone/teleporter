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
	bridgetoken "github.com/ava-labs/teleporter/abi-bindings/go/CrossChainApplications/ERC20Bridge/BridgeToken"
	teleportermessenger "github.com/ava-labs/teleporter/abi-bindings/go/Teleporter/TeleporterMessenger"
	teleporterregistry "github.com/ava-labs/teleporter/abi-bindings/go/Teleporter/upgrades/TeleporterRegistry"
	"github.com/ava-labs/teleporter/tests/interfaces"
	"github.com/ava-labs/teleporter/tests/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	. "github.com/onsi/gomega"
)

func ERC20CChainFromBridgeMultihop(network interfaces.Network) {
	cchainInfo, subnetBInfo, subnetCInfo := utils.GetThreeSubnets(network)
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
	subnetBTeleporterMessenger, err := teleportermessenger.NewTeleporterMessenger(
		teleporterContractAddress,
		subnetBInfo.RPCClient,
	)
	Expect(err).Should(BeNil())
	subnetCTeleporterMessenger, err := teleportermessenger.NewTeleporterMessenger(
		teleporterContractAddress,
		subnetCInfo.RPCClient,
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

	// Deploy an ERC20 to subnet A
	nativeERC20Address, nativeERC20 := utils.DeployExampleERC20(
		context.Background(),
		fundedKey,
		cchainInfo,
	)

	// Deploy the ERC20 bridge to subnet A
	erc20BridgeAddressA, erc20BridgeA := utils.DeployERC20Bridge(ctx, fundedKey, cchainInfo)
	// Deploy the ERC20 bridge to subnet B
	erc20BridgeAddressB, erc20BridgeB := utils.DeployERC20Bridge(ctx, fundedKey, subnetBInfo)
	// Deploy the ERC20 bridge to subnet C
	erc20BridgeAddressC, erc20BridgeC := utils.DeployERC20Bridge(ctx, fundedKey, subnetCInfo)

	amount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(10000000000000))
	utils.ERC20Approve(
		ctx,
		nativeERC20,
		erc20BridgeAddressA,
		amount,
		cchainInfo,
		fundedKey,
	)

	// Send a transaction on Subnet A to add support for the the ERC20 token to the bridge on Subnet B
	receipt, messageID := submitCreateBridgeToken(
		ctx,
		cchainInfo,
		subnetBInfo.BlockchainID,
		erc20BridgeAddressB,
		nativeERC20Address,
		nativeERC20Address,
		big.NewInt(0),
		fundedAddress,
		fundedKey,
		erc20BridgeA,
		cchainTeleporterMessenger,
	)

	// Relay message
	network.RelayMessage(ctx, receipt, cchainInfo, subnetBInfo, true)

	// Check Teleporter message received on the destination
	delivered, err := subnetBTeleporterMessenger.MessageReceived(
		&bind.CallOpts{},
		cchainInfo.BlockchainID,
		messageID,
	)
	Expect(err).Should(BeNil())
	Expect(delivered).Should(BeTrue())

	// Check the bridge token was added on Subnet B
	bridgeTokenSubnetBAddress, err := erc20BridgeB.NativeToWrappedTokens(
		&bind.CallOpts{},
		cchainInfo.BlockchainID,
		erc20BridgeAddressA,
		nativeERC20Address,
	)
	Expect(err).Should(BeNil())
	Expect(bridgeTokenSubnetBAddress).ShouldNot(Equal(common.Address{}))
	bridgeTokenB, err := bridgetoken.NewBridgeToken(bridgeTokenSubnetBAddress, subnetBInfo.RPCClient)
	Expect(err).Should(BeNil())

	// Check all the settings of the new bridge token are correct.
	actualNativeChainID, err := bridgeTokenB.NativeBlockchainID(&bind.CallOpts{})
	Expect(err).Should(BeNil())
	Expect(actualNativeChainID[:]).Should(Equal(cchainInfo.BlockchainID[:]))

	actualNativeBridgeAddress, err := bridgeTokenB.NativeBridge(&bind.CallOpts{})
	Expect(err).Should(BeNil())
	Expect(actualNativeBridgeAddress).Should(Equal(erc20BridgeAddressA))

	actualNativeAssetAddress, err := bridgeTokenB.NativeAsset(&bind.CallOpts{})
	Expect(err).Should(BeNil())
	Expect(actualNativeAssetAddress).Should(Equal(nativeERC20Address))

	actualName, err := bridgeTokenB.Name(&bind.CallOpts{})
	Expect(err).Should(BeNil())
	Expect(actualName).Should(Equal("Mock Token"))

	actualSymbol, err := bridgeTokenB.Symbol(&bind.CallOpts{})
	Expect(err).Should(BeNil())
	Expect(actualSymbol).Should(Equal("EXMP"))

	actualDecimals, err := bridgeTokenB.Decimals(&bind.CallOpts{})
	Expect(err).Should(BeNil())
	Expect(actualDecimals).Should(Equal(uint8(18)))

	// Send a transaction on Subnet A to add support for the the ERC20 token to the bridge on Subnet C
	receipt, messageID = submitCreateBridgeToken(
		ctx,
		cchainInfo,
		subnetCInfo.BlockchainID,
		erc20BridgeAddressC,
		nativeERC20Address,
		nativeERC20Address,
		big.NewInt(0),
		fundedAddress,
		fundedKey,
		erc20BridgeA,
		cchainTeleporterMessenger,
	)

	// Relay message
	network.RelayMessage(ctx, receipt, cchainInfo, subnetCInfo, true)

	// Check Teleporter message received on the destination
	delivered, err = subnetCTeleporterMessenger.MessageReceived(
		&bind.CallOpts{},
		cchainInfo.BlockchainID,
		messageID,
	)
	Expect(err).Should(BeNil())
	Expect(delivered).Should(BeTrue())

	// Check the bridge token was added on Subnet C
	bridgeTokenSubnetCAddress, err := erc20BridgeC.NativeToWrappedTokens(
		&bind.CallOpts{},
		cchainInfo.BlockchainID,
		erc20BridgeAddressA,
		nativeERC20Address,
	)
	Expect(err).Should(BeNil())
	Expect(bridgeTokenSubnetCAddress).ShouldNot(Equal(common.Address{}))
	bridgeTokenC, err := bridgetoken.NewBridgeToken(bridgeTokenSubnetCAddress, subnetCInfo.RPCClient)
	Expect(err).Should(BeNil())

	// Send a bridge transfer for the newly added token from subnet A to subnet B
	totalAmount := big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(13))
	primaryFeeAmount := big.NewInt(1e18)
	receipt, messageID = bridgeToken(
		ctx,
		cchainInfo,
		subnetBInfo.BlockchainID,
		erc20BridgeAddressB,
		nativeERC20Address,
		fundedAddress,
		totalAmount,
		primaryFeeAmount,
		big.NewInt(0),
		fundedAddress,
		fundedKey,
		erc20BridgeA,
		true,
		cchainInfo.BlockchainID,
		cchainTeleporterMessenger,
	)

	// Relay message
	deliveryReceipt := network.RelayMessage(ctx, receipt, cchainInfo, subnetBInfo, true)
	receiveEvent, err := utils.GetEventFromLogs(
		deliveryReceipt.Logs,
		subnetBInfo.TeleporterMessenger.ParseReceiveCrossChainMessage)
	Expect(err).Should(BeNil())

	// Check Teleporter message received on the destination
	delivered, err = subnetBTeleporterMessenger.MessageReceived(&bind.CallOpts{}, cchainInfo.BlockchainID, messageID)
	Expect(err).Should(BeNil())
	Expect(delivered).Should(BeTrue())

	// Check the recipient balance of the new bridge token.
	actualRecipientBalance, err := bridgeTokenB.BalanceOf(&bind.CallOpts{}, fundedAddress)
	Expect(err).Should(BeNil())
	Expect(actualRecipientBalance).Should(Equal(totalAmount.Sub(totalAmount, primaryFeeAmount)))

	// Approve the bridge contract on subnet B to spend the wrapped tokens in the user account.
	approveBridgeToken(
		ctx,
		subnetBInfo,
		bridgeTokenSubnetBAddress,
		bridgeTokenB,
		amount,
		erc20BridgeAddressB,
		fundedAddress,
		fundedKey,
	)

	// Check the initial relayer reward amount on SubnetA.
	currentRewardAmount, err := cchainInfo.TeleporterMessenger.CheckRelayerRewardAmount(
		&bind.CallOpts{},
		receiveEvent.RewardRedeemer,
		nativeERC20Address)
	Expect(err).Should(BeNil())

	// Unwrap bridged tokens back to subnet A, then wrap tokens to final destination on subnet C
	totalAmount = big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(11))
	secondaryFeeAmount := big.NewInt(1e18)
	receipt, messageID = bridgeToken(
		ctx,
		subnetBInfo,
		subnetCInfo.BlockchainID,
		erc20BridgeAddressC,
		bridgeTokenSubnetBAddress,
		fundedAddress,
		totalAmount,
		primaryFeeAmount,
		secondaryFeeAmount,
		fundedAddress,
		fundedKey,
		erc20BridgeB,
		false,
		cchainInfo.BlockchainID,
		subnetBTeleporterMessenger,
	)

	// Relay message from SubnetB to SubnetA
	// The receipt of transaction that delivers the message will also have the "second hop"
	// message sent from subnet A to subnet C.
	receipt = network.RelayMessage(ctx, receipt, subnetBInfo, cchainInfo, true)

	// Check Teleporter message received on the destination
	delivered, err = cchainTeleporterMessenger.MessageReceived(
		&bind.CallOpts{},
		subnetBInfo.BlockchainID,
		messageID,
	)
	Expect(err).Should(BeNil())
	Expect(delivered).Should(BeTrue())

	// Get the sendCrossChainMessage event from SubnetA to SubnetC, which should be present
	// the receipt of the transaction that delivered the first message from SubnetB to SubnetA.
	event, err := utils.GetEventFromLogs(receipt.Logs, cchainTeleporterMessenger.ParseSendCrossChainMessage)
	Expect(err).Should(BeNil())
	Expect(event.DestinationBlockchainID[:]).Should(Equal(subnetCInfo.BlockchainID[:]))
	messageID = event.Message.MessageID

	// Check the redeemable reward balance of the relayer if the relayer address was set
	updatedRewardAmount, err := cchainTeleporterMessenger.CheckRelayerRewardAmount(
		&bind.CallOpts{},
		receiveEvent.RewardRedeemer,
		nativeERC20Address,
	)
	Expect(err).Should(BeNil())
	Expect(updatedRewardAmount).Should(Equal(new(big.Int).Add(currentRewardAmount, primaryFeeAmount)))

	// Relay message from SubnetA to SubnetC
	deliveryReceipt = network.RelayMessage(ctx, receipt, cchainInfo, subnetCInfo, true)
	receiveEvent, err = utils.GetEventFromLogs(
		deliveryReceipt.Logs,
		subnetCInfo.TeleporterMessenger.ParseReceiveCrossChainMessage)
	Expect(err).Should(BeNil())

	// Check Teleporter message received on the destination
	delivered, err = subnetCTeleporterMessenger.MessageReceived(&bind.CallOpts{}, cchainInfo.BlockchainID, messageID)
	Expect(err).Should(BeNil())
	Expect(delivered).Should(BeTrue())

	actualRecipientBalance, err = bridgeTokenC.BalanceOf(&bind.CallOpts{}, fundedAddress)
	Expect(err).Should(BeNil())
	expectedAmount := totalAmount.Sub(totalAmount, primaryFeeAmount).Sub(totalAmount, secondaryFeeAmount)
	Expect(actualRecipientBalance).Should(Equal(expectedAmount))

	// Approve the bridge contract on Subnet C to spend the bridge tokens from the user account
	approveBridgeToken(
		ctx,
		subnetCInfo,
		bridgeTokenSubnetCAddress,
		bridgeTokenC,
		amount,
		erc20BridgeAddressC,
		fundedAddress,
		fundedKey)

	// Get the current relayer reward amount on SubnetA.
	currentRewardAmount, err = cchainInfo.TeleporterMessenger.CheckRelayerRewardAmount(
		&bind.CallOpts{},
		receiveEvent.RewardRedeemer,
		nativeERC20Address)
	Expect(err).Should(BeNil())

	// Send a transaction to unwrap tokens from Subnet C back to Subnet A
	totalAmount = big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(8))
	receipt, messageID = bridgeToken(
		ctx,
		subnetCInfo,
		cchainInfo.BlockchainID,
		erc20BridgeAddressA,
		bridgeTokenSubnetCAddress,
		fundedAddress,
		totalAmount,
		primaryFeeAmount,
		big.NewInt(0),
		fundedAddress,
		fundedKey,
		erc20BridgeC,
		false,
		cchainInfo.BlockchainID,
		subnetCTeleporterMessenger,
	)

	// Relay message from SubnetC to SubnetA
	network.RelayMessage(ctx, receipt, subnetCInfo, cchainInfo, true)

	// Check Teleporter message received on the destination
	delivered, err = cchainTeleporterMessenger.MessageReceived(&bind.CallOpts{}, subnetCInfo.BlockchainID, messageID)
	Expect(err).Should(BeNil())
	Expect(delivered).Should(BeTrue())

	// Check the balance of the native token after the unwrap
	actualNativeTokenDefaultAccountBalance, err := nativeERC20.BalanceOf(&bind.CallOpts{}, fundedAddress)
	Expect(err).Should(BeNil())
	expectedAmount = big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(9999999994))
	Expect(actualNativeTokenDefaultAccountBalance).Should(Equal(expectedAmount))

	// Check the balance of the native token for the relayer, which should have received the fee rewards
	updatedRewardAmount, err = cchainTeleporterMessenger.CheckRelayerRewardAmount(
		&bind.CallOpts{},
		receiveEvent.RewardRedeemer,
		nativeERC20Address,
	)
	Expect(err).Should(BeNil())
	Expect(updatedRewardAmount).Should(Equal(new(big.Int).Add(currentRewardAmount, secondaryFeeAmount)))
}
