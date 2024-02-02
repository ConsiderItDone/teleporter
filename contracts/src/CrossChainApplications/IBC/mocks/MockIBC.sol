// SPDX-License-Identifier: Ecosystem
pragma solidity 0.8.18;

import "../IIBC.sol";

contract MockIBC is IIBC {
  uint private sendPacketCalled;

  function getSendPacketCalled() public view returns (uint) {
    return sendPacketCalled;
  }

  function sendPacket(
    uint,
    string memory,
    string memory,
    Height memory,
    uint,
    bytes memory
  ) public override {
    sendPacketCalled += 1;
  }
}