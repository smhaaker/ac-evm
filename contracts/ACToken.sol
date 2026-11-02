// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ArithmeticDecoder} from "./ArithmeticDecoder.sol";

/// @notice Soulbound (ERC-5192) ERC-721 holding arithmetic-coded text, decoded
/// on-chain in read/tokenURI. Mint directly or via an EIP-712 signature.
contract ACToken {
    string public constant name = "ac-evm";
    string public constant symbol = "ACEVM";
    uint256 private constant MAX_OUT = 1000; // ~29M gas to decode; under typical eth_call caps

    bytes32 private constant DOMAIN_TYPEHASH =
        keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)");
    bytes32 private constant MINT_TYPEHASH = keccak256("Mint(address author,bytes blob,uint256 nonce,uint256 deadline)");
    // secp256k1n / 2, upper bound for s (no malleable signatures)
    uint256 private constant HALF_N = 0x7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0;

    ArithmeticDecoder public immutable decoder;

    mapping(uint256 => address) private _owner;
    mapping(address => uint256) private _balance;
    mapping(uint256 => bytes) private _blob;
    mapping(address => uint256) public nonces;
    uint256 public totalSupply; // ids start at 1

    event Transfer(address indexed from, address indexed to, uint256 indexed id);
    event Locked(uint256 tokenId);

    error Nonexistent();
    error ZeroAddress();
    error Soulbound();
    error EmptyBlob();
    error Expired();
    error BadSignature();

    constructor(ArithmeticDecoder d) {
        decoder = d;
    }

    function mint(bytes calldata blob) external returns (uint256) {
        return _mint(msg.sender, blob);
    }

    /// @notice Anyone may submit; the token always goes to `author`.
    function mintWithSig(address author, bytes calldata blob, uint256 deadline, bytes calldata sig)
        external
        returns (uint256)
    {
        if (block.timestamp > deadline) revert Expired();
        bytes32 digest = keccak256(
            abi.encodePacked(
                "\x19\x01",
                domainSeparator(),
                keccak256(abi.encode(MINT_TYPEHASH, author, keccak256(blob), nonces[author]++, deadline))
            )
        );
        if (!_validSig(author, digest, sig)) revert BadSignature();
        return _mint(author, blob);
    }

    function domainSeparator() public view returns (bytes32) {
        return keccak256(abi.encode(DOMAIN_TYPEHASH, keccak256("ac-evm"), keccak256("1"), block.chainid, address(this)));
    }

    function read(uint256 id) public view returns (string memory) {
        _requireExists(id);
        return string(decoder.decompressPacked(_blob[id], MAX_OUT));
    }

    function compressedOf(uint256 id) external view returns (bytes memory) {
        _requireExists(id);
        return _blob[id];
    }

    function tokenURI(uint256 id) external view returns (string memory) {
        string memory json = string.concat(
            '{"name":"ac-evm #', _toString(id), '","description":"', _escapeJSON(bytes(read(id))), '"}'
        );
        return string.concat("data:application/json;base64,", _base64(bytes(json)));
    }

    function locked(uint256 id) external view returns (bool) {
        _requireExists(id);
        return true;
    }

    function balanceOf(address o) external view returns (uint256) {
        if (o == address(0)) revert ZeroAddress();
        return _balance[o];
    }

    function ownerOf(uint256 id) public view returns (address o) {
        o = _owner[id];
        if (o == address(0)) revert Nonexistent();
    }

    function getApproved(uint256 id) external view returns (address) {
        _requireExists(id);
        return address(0);
    }

    function isApprovedForAll(address, address) external pure returns (bool) {
        return false;
    }

    function approve(address, uint256) external pure {
        revert Soulbound();
    }

    function setApprovalForAll(address, bool) external pure {
        revert Soulbound();
    }

    function transferFrom(address, address, uint256) external pure {
        revert Soulbound();
    }

    function safeTransferFrom(address, address, uint256) external pure {
        revert Soulbound();
    }

    function safeTransferFrom(address, address, uint256, bytes calldata) external pure {
        revert Soulbound();
    }

    function supportsInterface(bytes4 iid) external pure returns (bool) {
        return iid == 0x01ffc9a7 // ERC165
            || iid == 0x80ac58cd // ERC721
            || iid == 0x5b5e139f // ERC721Metadata
            || iid == 0xb45a3c0e; // ERC5192
    }

    function _mint(address to, bytes calldata blob) private returns (uint256 id) {
        if (blob.length == 0) revert EmptyBlob();
        id = ++totalSupply;
        _blob[id] = blob;
        _owner[id] = to;
        unchecked {
            _balance[to]++;
        }
        emit Transfer(address(0), to, id);
        emit Locked(id);
    }

    function _requireExists(uint256 id) private view {
        if (_owner[id] == address(0)) revert Nonexistent();
    }

    function _validSig(address signer, bytes32 digest, bytes calldata sig) private pure returns (bool) {
        if (sig.length != 65) return false;
        bytes32 r = bytes32(sig[0:32]);
        bytes32 s = bytes32(sig[32:64]);
        uint8 v = uint8(sig[64]);
        if (uint256(s) > HALF_N || (v != 27 && v != 28)) return false;
        address got = ecrecover(digest, v, r, s);
        return got != address(0) && got == signer;
    }

    function _toString(uint256 v) private pure returns (string memory) {
        if (v == 0) return "0";
        uint256 digits;
        for (uint256 n = v; n != 0; n /= 10) {
            digits++;
        }
        bytes memory b = new bytes(digits);
        for (; v != 0; v /= 10) {
            b[--digits] = bytes1(uint8(48 + (v % 10)));
        }
        return string(b);
    }

    function _escapeJSON(bytes memory s) private pure returns (string memory) {
        bytes memory out = new bytes(s.length * 6); // worst case \u00XX
        uint256 j = 0;
        for (uint256 i = 0; i < s.length; i++) {
            bytes1 c = s[i];
            if (c == '"' || c == "\\") {
                out[j++] = "\\";
                out[j++] = c;
            } else if (c == 0x0a) {
                out[j++] = "\\";
                out[j++] = "n";
            } else if (uint8(c) < 0x20) {
                out[j++] = "\\";
                out[j++] = "u";
                out[j++] = "0";
                out[j++] = "0";
                out[j++] = _hex(uint8(c) >> 4);
                out[j++] = _hex(uint8(c) & 0x0f);
            } else {
                out[j++] = c;
            }
        }
        assembly ("memory-safe") {
            mstore(out, j)
        }
        return string(out);
    }

    function _hex(uint8 v) private pure returns (bytes1) {
        return bytes1(v < 10 ? 48 + v : 87 + v);
    }

    function _base64(bytes memory data) private pure returns (string memory) {
        bytes memory T = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
        uint256 len = data.length;
        bytes memory out = new bytes(4 * ((len + 2) / 3));
        uint256 j = 0;
        for (uint256 i = 0; i < len; i += 3) {
            uint256 n = uint256(uint8(data[i])) << 16;
            if (i + 1 < len) n |= uint256(uint8(data[i + 1])) << 8;
            if (i + 2 < len) n |= uint256(uint8(data[i + 2]));
            out[j++] = T[(n >> 18) & 63];
            out[j++] = T[(n >> 12) & 63];
            out[j++] = i + 1 < len ? T[(n >> 6) & 63] : bytes1("=");
            out[j++] = i + 2 < len ? T[n & 63] : bytes1("=");
        }
        return string(out);
    }
}
