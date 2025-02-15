// (c) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// SPDX-License-Identifier: Ecosystem

pragma solidity 0.8.18;

import {ReentrancyGuard} from "@openzeppelin/contracts/security/ReentrancyGuard.sol";
import {Address} from "@openzeppelin/contracts/utils/Address.sol";
import {IWarpMessenger} from "@subnet-evm-contracts/interfaces/IWarpMessenger.sol";
import {INativeMinter} from "@subnet-evm-contracts/interfaces/INativeMinter.sol";
import {INativeTokenDestination} from "./INativeTokenDestination.sol";
import {ITokenSource} from "./ITokenSource.sol";
import {ITeleporterMessenger, TeleporterFeeInfo, TeleporterMessageInput} from "../../Teleporter/ITeleporterMessenger.sol";
import {ITeleporterReceiver} from "../../Teleporter/ITeleporterReceiver.sol";
import {SafeERC20TransferFrom} from "../../Teleporter/SafeERC20TransferFrom.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
// We need IAllowList as an indirect dependency in order to compile.
// solhint-disable-next-line no-unused-import
import {IAllowList} from "@subnet-evm-contracts/interfaces/IAllowList.sol";

contract NativeTokenDestination is
    ITeleporterReceiver,
    INativeTokenDestination,
    ReentrancyGuard
{
    // The address where the burned transaction fees are credited.    
    // Defined as BLACKHOLE_ADDRESS at 
    // https://github.com/ava-labs/subnet-evm/blob/e23ab058d039ff9c8469c89b139d21d52c4bd283/constants/constants.go
    address public constant BURNED_TX_FEES_ADDRESS = 0x0100000000000000000000000000000000000000;
    // Designated Blackhole Address for this contract. Tokens are sent here to be "burned" before 
    // sending an unlock message to the source chain. Different from the burned tx fee address so 
    // they can be tracked separately.
    address public constant BURN_FOR_TRANSFER_ADDRESS = 0x0100000000000000000000000000000000000001;

    INativeMinter private immutable _nativeMinter =
        INativeMinter(0x0200000000000000000000000000000000000001);

    uint256 public constant TRANSFER_NATIVE_TOKENS_REQUIRED_GAS = 100_000;
    uint256 public constant REPORT_BURNED_TOKENS_REQUIRED_GAS = 100_000;
    bytes32 public immutable sourceBlockchainID;
    address public immutable nativeTokenSourceAddress;
    // The first `initialReserveImbalance` tokens sent to this subnet will not be minted.
    // `initialReserveImbalance` should be constructed to match the initial token supply of this subnet.
    // This means tokens will not be minted until the source contact is collateralized.
    uint256 public immutable initialReserveImbalance;
    uint256 public currentReserveImbalance;
    uint256 public totalMinted;

    // Used for sending and receiving Teleporter messages.
    ITeleporterMessenger public immutable teleporterMessenger;

    constructor(
        address teleporterMessengerAddress,
        bytes32 sourceBlockchainID_,
        address nativeTokenSourceAddress_,
        uint256 initialReserveImbalance_
    ) {
        require(
            teleporterMessengerAddress != address(0),
            "NativeTokenDestination: zero TeleporterMessenger address"
        );
        teleporterMessenger = ITeleporterMessenger(teleporterMessengerAddress);

        require(
            sourceBlockchainID_ != bytes32(0),
            "NativeTokenDestination: zero source blockchain ID"
        );
        require(
            sourceBlockchainID_ !=
                IWarpMessenger(0x0200000000000000000000000000000000000005)
                    .getBlockchainID(),
            "NativeTokenDestination: cannot bridge with same blockchain"
        );
        sourceBlockchainID = sourceBlockchainID_;

        require(
            nativeTokenSourceAddress_ != address(0),
            "NativeTokenDestination: zero source contract address"
        );
        nativeTokenSourceAddress = nativeTokenSourceAddress_;

        require(
            initialReserveImbalance_ != 0,
            "NativeTokenDestination: zero initial reserve imbalance"
        );

        initialReserveImbalance = initialReserveImbalance_;
        currentReserveImbalance = initialReserveImbalance_;
    }

    /**
     * @dev See {ITeleporterReceiver-receiveTeleporterMessage}.
     *
     * Receives a Teleporter message.
     */
    function receiveTeleporterMessage(
        bytes32 senderBlockchainID,
        address senderAddress,
        bytes calldata message
    ) external nonReentrant {
        // Only allow the Teleporter messenger to deliver messages.
        require(
            msg.sender == address(teleporterMessenger),
            "NativeTokenDestination: unauthorized TeleporterMessenger contract"
        );

        // Only allow messages from the source chain.
        require(
            senderBlockchainID == sourceBlockchainID,
            "NativeTokenDestination: invalid source chain"
        );

        // Only allow the partner contract to send messages.
        require(
            senderAddress == nativeTokenSourceAddress,
            "NativeTokenDestination: unauthorized sender"
        );

        (address recipient, uint256 amount) = abi.decode(
            message,
            (address, uint256)
        );
        require(
            recipient != address(0),
            "NativeTokenDestination: zero recipient address"
        );
        require(amount != 0, "NativeTokenDestination: zero transfer value");

        // If the contract has not yet been collateralized, we will deduct as many tokens
        // as needed from the transfer as needed. If there are any excess tokens, they will
        // be minted and sent to the recipient.
        uint256 adjustedAmount = amount;
        if (currentReserveImbalance > 0) {
            if (amount > currentReserveImbalance) {
                emit CollateralAdded({
                    amount: currentReserveImbalance,
                    remaining: 0
                });
                adjustedAmount = amount - currentReserveImbalance;
                currentReserveImbalance = 0;
            } else {
                currentReserveImbalance -= amount;
                emit CollateralAdded({
                    amount: amount,
                    remaining: currentReserveImbalance
                });
                return;
            }
        }

        totalMinted += adjustedAmount;
        emit NativeTokensMinted(recipient, adjustedAmount);
        // Calls NativeMinter precompile through INativeMinter interface.
        _nativeMinter.mintNativeCoin(recipient, adjustedAmount);
    }

    /**
     * @dev See {INativeTokenDestination-transferToSource}.
     */
    function transferToSource(
        address recipient,
        TeleporterFeeInfo calldata feeInfo,
        address[] calldata allowedRelayerAddresses
    ) external payable nonReentrant {
        // The recipient cannot be the zero address.
        require(
            recipient != address(0),
            "NativeTokenDestination: zero recipient address"
        );

        require(
            currentReserveImbalance == 0,
            "NativeTokenDestination: contract undercollateralized"
        );

        // Lock tokens in this bridge instance. Supports "fee/burn on transfer" ERC20 token
        // implementations by only bridging the actual balance increase reflected by the call
        // to transferFrom.
        uint256 adjustedFeeAmount;
        if (feeInfo.amount > 0) {
            adjustedFeeAmount = SafeERC20TransferFrom.safeTransferFrom(
                IERC20(feeInfo.feeTokenAddress),
                feeInfo.amount
            );
            SafeERC20.safeIncreaseAllowance(
                IERC20(feeInfo.feeTokenAddress),
                address(teleporterMessenger),
                adjustedFeeAmount
            );
        }

        // Burn native token by sending to BURN_FOR_TRANSFER_ADDRESS
        Address.sendValue(payable(BURN_FOR_TRANSFER_ADDRESS), msg.value);

        uint256 messageID = teleporterMessenger.sendCrossChainMessage(
            TeleporterMessageInput({
                destinationBlockchainID: sourceBlockchainID,
                destinationAddress: nativeTokenSourceAddress,
                feeInfo: feeInfo,
                requiredGasLimit: TRANSFER_NATIVE_TOKENS_REQUIRED_GAS,
                allowedRelayerAddresses: allowedRelayerAddresses,
                message: abi.encode(
                    ITokenSource.SourceAction.Unlock,
                    abi.encode(recipient, msg.value)
                )
            })
        );

        emit TransferToSource({
            sender: msg.sender,
            recipient: recipient,
            amount: msg.value,
            teleporterMessageID: messageID
        });
    }

    /**
     * @dev See {INativeTokenDestination-reportTotalBurnedTxFees}.
     */
    function reportTotalBurnedTxFees(
        TeleporterFeeInfo calldata feeInfo,
        address[] calldata allowedRelayerAddresses
    ) external {
        uint256 totalBurnedTxFees = address(BURNED_TX_FEES_ADDRESS).balance;
        uint256 messageID = teleporterMessenger.sendCrossChainMessage(
            TeleporterMessageInput({
                destinationBlockchainID: sourceBlockchainID,
                destinationAddress: nativeTokenSourceAddress,
                feeInfo: feeInfo,
                requiredGasLimit: REPORT_BURNED_TOKENS_REQUIRED_GAS,
                allowedRelayerAddresses: allowedRelayerAddresses,
                message: abi.encode(
                    ITokenSource.SourceAction.Burn,
                    abi.encode(totalBurnedTxFees)
                )
            })
        );

        emit ReportTotalBurnedTxFees({
            burnAddressBalance: totalBurnedTxFees,
            teleporterMessageID: messageID
        });
    }

    /**
     * @dev See {INativeTokenDestination-isCollateralized}.
     */
    function isCollateralized() external view returns (bool) {
        return currentReserveImbalance == 0;
    }

    /**
     * @dev See {INativeTokenDestination-totalSupply}.
     */
    function totalSupply() external view returns (uint256) {
        uint256 burned = address(BURNED_TX_FEES_ADDRESS).balance +
            address(BURN_FOR_TRANSFER_ADDRESS).balance;
        uint256 created = totalMinted + initialReserveImbalance;

        return created - burned;
    }
}
