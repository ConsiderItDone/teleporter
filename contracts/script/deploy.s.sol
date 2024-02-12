// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.13;

import "forge-std/Script.sol";
import "../src/Mocks/ExampleERC20.sol";
import "../src/Teleporter/TeleporterMessenger.sol";
import "../src/Teleporter/upgrades/TeleporterRegistry.sol";
import "../src/CrossChainApplications/ERC20Bridge/ERC20Bridge.sol";
import "../src/CrossChainApplications/ERC20Bridge/ICS20Bridge.sol";

contract DeployERC20Bridge is Script {
    ProtocolRegistryEntry[] initialEntries;

    function run() external {
        uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        vm.startBroadcast(deployerPrivateKey);

        ExampleERC20 token1 = new ExampleERC20();
        ExampleERC20 token2 = new ExampleERC20();
        ExampleERC20 token3 = new ExampleERC20();

        TeleporterMessenger teleporterMessenger = new TeleporterMessenger();

        initialEntries.push(ProtocolRegistryEntry({
            version: 1,
            protocolAddress: address(teleporterMessenger)
        }));
        TeleporterRegistry teleporterRegistry = new TeleporterRegistry(initialEntries);

        ERC20Bridge erc20Bridge = new ERC20Bridge(address(teleporterRegistry));

        vm.stopBroadcast();
    }
}

contract DeployICS20Bridge is Script {
    ProtocolRegistryEntry[] initialEntries;

    function run() external {
        uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        vm.startBroadcast(deployerPrivateKey);

        ExampleERC20 token1 = new ExampleERC20();
        ExampleERC20 token2 = new ExampleERC20();
        ExampleERC20 token3 = new ExampleERC20();

        TeleporterMessenger teleporterMessenger = new TeleporterMessenger();

        initialEntries.push(ProtocolRegistryEntry({
            version: 1,
            protocolAddress: address(teleporterMessenger)
        }));
        TeleporterRegistry teleporterRegistry = new TeleporterRegistry(initialEntries);

        ICS20Bridge erc20Bridge = new ICS20Bridge(address(teleporterRegistry), "some_channel");

        vm.stopBroadcast();
    }
}