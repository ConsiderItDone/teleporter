// (c) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// SPDX-License-Identifier: Ecosystem

pragma solidity 0.8.18;

/**
 * @dev Interface for common functionality needed for different `TokenSource` contracts such as 
 * `NativeTokenSource` and `ERC20TokenSource`.
 */
interface ITokenSource {
    /**
     * @dev Enum representing the action to take on receiving a Teleporter message.
     */
    enum SourceAction {
        Unlock,
        Burn
    }
    
    /**
     * @dev Emitted when native tokens are locked in the source contract to be transferred to the destination chain.
     */
    event TransferToDestination(
        address indexed sender,
        address indexed recipient,
        uint256 indexed teleporterMessageID,
        uint256 amount
    );

    /**
     * @dev Emitted when tokens are unlocked on this chain.
     */
    event UnlockTokens(address recipient, uint256 amount);

    /**
     * @dev Emitted when tokens are burned on this chain.
     */
    event BurnTokens(uint256 amount);
}