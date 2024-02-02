// SPDX-License-Identifier: Ecosystem
pragma solidity 0.8.18;

struct FungibleTokenPacketData {
  string denom;
  uint256 amount;
  bytes sender;
  bytes receiver;
  bytes memo;
}

struct Packet {
  uint sequence;
  string sourcePort;
  string sourceChannel;
  string destinationPort;
  string destinationChannel;
  bytes data;
  Height timeoutHeight;
  uint timeoutTimestamp;
}

struct Height {
  uint revisionNumber;
  uint revisionHeight;
}

interface IIBC {
  function sendPacket(
    uint channelCapability,
    string memory sourcePort,
    string memory sourceChannel,
    Height memory timeoutHeight,
    uint timeoutTimestamp,
    bytes memory data
  ) external;
}