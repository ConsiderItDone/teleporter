// (c) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// SPDX-License-Identifier: Ecosystem

pragma solidity 0.8.18;

import {ReentrancyGuard} from "@openzeppelin/contracts/security/ReentrancyGuard.sol";
import {Address} from "@openzeppelin/contracts/utils/Address.sol";
import {IWarpMessenger} from "@subnet-evm-contracts/interfaces/IWarpMessenger.sol";
import {INativeTokenSource} from "./INativeTokenSource.sol";
import {ITokenSource} from "./ITokenSource.sol";
import {ITeleporterMessenger, TeleporterFeeInfo, TeleporterMessageInput} from "../../Teleporter/ITeleporterMessenger.sol";
import {ITeleporterReceiver} from "../../Teleporter/ITeleporterReceiver.sol";
import {SafeERC20TransferFrom} from "../../Teleporter/SafeERC20TransferFrom.sol";
import {SafeERC20TransferFrom} from "../../Teleporter/SafeERC20TransferFrom.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

contract NativeTokenSource is
    ITeleporterReceiver,
    INativeTokenSource,
    ITokenSource,
    ReentrancyGuard
{
    // The address where the burned transaction fees are credited.
    // Defined as BLACKHOLE_ADDRESS at
    // https://github.com/ava-labs/subnet-evm/blob/e23ab058d039ff9c8469c89b139d21d52c4bd283/constants/constants.go
    address public constant BURNED_TX_FEES_ADDRESS =
        0x0100000000000000000000000000000000000000;
    uint256 public constant MINT_NATIVE_TOKENS_REQUIRED_GAS = 100_000;
    // Used to keep track of tokens burned through transactions on the destination chain. They can
    // be reported to this contract to burn an equivalent number of tokens on this chain.
    uint256 public destinationBurnedTotal;
    bytes32 public immutable destinationBlockchainID;
    address public immutable nativeTokenDestinationAddress;

    // Used for sending and receiving Teleporter messages.
    ITeleporterMessenger public immutable teleporterMessenger;

    constructor(
        address teleporterMessengerAddress,
        bytes32 destinationBlockchainID_,
        address nativeTokenDestinationAddress_
    ) {
        require(
            teleporterMessengerAddress != address(0),
            "NativeTokenSource: zero TeleporterMessenger address"
        );
        teleporterMessenger = ITeleporterMessenger(teleporterMessengerAddress);

        require(
            destinationBlockchainID_ != bytes32(0),
            "NativeTokenSource: zero destination blockchain ID"
        );
        require(
            destinationBlockchainID_ !=
                IWarpMessenger(0x0200000000000000000000000000000000000005)
                    .getBlockchainID(),
            "NativeTokenSource: cannot bridge with same blockchain"
        );
        destinationBlockchainID = destinationBlockchainID_;

        require(
            nativeTokenDestinationAddress_ != address(0),
            "NativeTokenSource: zero destination contract address"
        );
        nativeTokenDestinationAddress = nativeTokenDestinationAddress_;
    }

    /**
     * @dev See {ITeleporterReceiver-receiveTeleporterMessage}.
     *
     * Receives a Teleporter message and routes to the appropriate internal function call.
     */
    function receiveTeleporterMessage(
        bytes32 senderBlockchainID,
        address senderAddress,
        bytes calldata message
    ) external nonReentrant {
        // Only allow the Teleporter messenger to deliver messages.
        require(
            msg.sender == address(teleporterMessenger),
            "NativeTokenSource: unauthorized TeleporterMessenger contract"
        );

        // Only allow messages from the destination chain.
        require(
            senderBlockchainID == destinationBlockchainID,
            "NativeTokenSource: invalid destination chain"
        );

        // Only allow the partner contract to send messages.
        require(
            senderAddress == nativeTokenDestinationAddress,
            "NativeTokenSource: unauthorized sender"
        );

        // Decode the payload to recover the action and corresponding function parameters
        (SourceAction action, bytes memory actionData) = abi.decode(
            message,
            (SourceAction, bytes)
        );

        // Route to the appropriate function.
        if (action == SourceAction.Unlock) {
            (address recipient, uint256 amount) = abi.decode(
                actionData,
                (address, uint256)
            );
            _unlockTokens(recipient, amount);
        } else if (action == SourceAction.Burn) {
            uint256 newBurnTotal = abi.decode(actionData, (uint256));
            _handleBurnTokens(newBurnTotal);
        } else {
            revert("NativeTokenSource: invalid action");
        }
    }

    /**
     * @dev See {INativeTokenSource-transferToDestination}.
     */
    function transferToDestination(
        address recipient,
        TeleporterFeeInfo calldata feeInfo,
        address[] calldata allowedRelayerAddresses
    ) external payable nonReentrant {
        // The recipient cannot be the zero address.
        require(
            recipient != address(0),
            "NativeTokenSource: zero recipient address"
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

        uint256 messageID = teleporterMessenger.sendCrossChainMessage(
            TeleporterMessageInput({
                destinationBlockchainID: destinationBlockchainID,
                destinationAddress: nativeTokenDestinationAddress,
                feeInfo: feeInfo,
                requiredGasLimit: MINT_NATIVE_TOKENS_REQUIRED_GAS,
                allowedRelayerAddresses: allowedRelayerAddresses,
                message: abi.encode(recipient, msg.value)
            })
        );

        emit TransferToDestination({
            sender: msg.sender,
            recipient: recipient,
            amount: msg.value,
            teleporterMessageID: messageID
        });
    }

    /**
     * @dev Unlocks tokens to recipient.
     */
    function _unlockTokens(address recipient, uint256 amount) private {
        require(
            recipient != address(0),
            "NativeTokenSource: zero recipient address"
        );
        require(
            address(this).balance >= amount,
            "NativeTokenSource: insufficient collateral"
        );

        // Transfer to recipient
        emit UnlockTokens(recipient, amount);
        Address.sendValue(payable(recipient), amount);
    }

    /**
     * @dev Sends tokens to BURNED_TX_FEES_ADDRESS.
     */
    function _burnTokens(uint256 amount) private {
        emit BurnTokens(amount);
        Address.sendValue(payable(BURNED_TX_FEES_ADDRESS), amount);
    }

    /**
     * @dev Update destinationBurnedTotal sent from destination chain
     * If the new burned total is less than the highest known burned total, this transaction is a no-op.
     * The burned total on the destination will only ever increase, but new totals may be relayed to this
     * chain out of order.
     */
    function _handleBurnTokens(uint256 newBurnTotal) private {
        if (newBurnTotal > destinationBurnedTotal) {
            uint256 difference = newBurnTotal - destinationBurnedTotal;
            _burnTokens(difference);
            destinationBurnedTotal = newBurnTotal;
        }
    }
}
