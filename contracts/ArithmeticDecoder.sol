// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @notice On-chain mirror of Go internal/ac: WNC arithmetic coder + adaptive
/// order-1 model. Must stay bit-identical to the Go side.
contract ArithmeticDecoder {
    uint256 private constant TOP = 1 << 32;
    uint256 private constant HALF = TOP >> 1;
    uint256 private constant QUARTER = TOP >> 2;
    uint256 private constant THREEQ = 3 * QUARTER;
    uint256 private constant MASK = TOP - 1;

    uint256 private constant NSYMS = 257; // 256 bytes + EOF
    uint256 private constant SYM_EOF = 256;
    uint32 private constant INCREMENT = 32;
    uint32 private constant RESCALE_AT = 1 << 15;

    uint8 private constant PRIOR_COLD = 0;
    uint8 private constant PRIOR_ENGLISH = 1;

    /// @dev Byte-identical to Go ac.DefaultPrior.
    bytes private constant PRIOR =
        hex"546865206d6f726e696e67207761732071756965742c20616e6420746865206c696768742063616d6520696e20736c6f776c79206f7665722074686520726f6f66732e2049206d61646520636f666665652c206f70656e6564207468652077696e646f772c20616e642077726f746520746865206669727374206c696e65206f662061206c6574746572204920686164206265656e206d65616e696e6720746f2073656e6420666f72207765656b732e20497420697320737472616e676520686f772074686520736d616c6c657374207468696e67732061726520746865206f6e657320776520707574206f666620746865206c6f6e676573742e0a0a54686572652069732061206b696e64206f662077726974696e672074686174206f6e6c7920776f726b73207768656e2069742069732073686f72742e2041206e6f7465206c656674206f6e2061207461626c652e2041206c696e65206f6e20746865206261636b206f6620612070686f746f2e2054776f20776f7264732061742074686520656e64206f662061206c6f6e67206461793a207468616e6b20796f752e205765207361792077686174206d6174746572732c20616e642077652073746f702c20616e6420746865207265616465722066696c6c7320696e2074686520726573742e0a0a536f6d65206461797320617265206d616465206f66206e756d626572733a2033206d656574696e67732c20313220656d61696c732c20312077616c6b20696e20746865207061726b206265666f7265206974207261696e65642e204f74686572206461797320617265206d616465206f662070656f706c652e20596f752063616c6c206120667269656e642c20616e642074686520686f757220676f657320627920776974686f757420656974686572206f6620796f75206e6f746963696e672e2054686f73652061726520746865206461797320796f752072656d656d6265722e0a0a676d20746f2065766572796f6e652077686f206973207570206561726c792e205468697320776173207772697474656e20746f206c6173742c20616e642069742077696c6c2062652068657265207768656e20796f7520636f6d65206261636b20746f207265616420697420616761696e2e204e6f7468696e6720696e206974206973207365637265742e2049742069732073696d706c79206b6570742c207468652077617920796f75206b65657020612073746f6e652066726f6d2061206265616368206f722061207469636b65742066726f6d20612073686f772e0a0a496620796f75206172652072656164696e6720746869732c204920686f7065207468652077656174686572206973206b696e6420776865726520796f75206172652e204920686f70652074686520776f726b20697320676f696e672077656c6c2c20616e6420746861742074686572652069732074696d6520666f722061206c6f6e672064696e6e65722077697468207468652070656f706c6520796f75206c6f76652e20577269746520736f6d657468696e6720646f776e20746f6461792e204d616b6520697420736d616c6c2c206d616b6520697420747275652c20616e64206c6574206974207374616e64206f6e20697473206f776e2e0a0a5768617420776f756c6420796f752073617920696620796f75206f6e6c792068616420612073656e74656e63653f204d617962652074686174206973207468652077686f6c6520706f696e74206f662069742e";

    error Corrupt();
    error TooLong();
    error UnknownPrior(uint8 id);

    /// @notice Decode blob = priorID(1) | coded bytes. Mirrors Go ac.Unpack.
    function decompressPacked(bytes memory blob, uint256 limit) public pure returns (bytes memory) {
        if (blob.length == 0) revert Corrupt();
        uint8 id = uint8(blob[0]);
        if (id == PRIOR_COLD) return _decode(blob, limit, false);
        if (id == PRIOR_ENGLISH) return _decode(blob, limit, true);
        revert UnknownPrior(id);
    }

    function _decode(bytes memory data, uint256 limit, bool warm) private pure returns (bytes memory) {
        uint256 low = 0;
        uint256 high = MASK;
        uint256 value = 0;
        uint256 bitpos = 8; // skip the prior-id byte
        for (uint256 i = 0; i < 32; i++) {
            value = (value << 1) | _readBit(data, bitpos);
            bitpos++;
        }

        // Rows are allocated on first use; untouched rows are uniform.
        uint32[][] memory rows = new uint32[][](NSYMS);
        uint32[] memory totals = new uint32[](NSYMS);
        for (uint256 c = 0; c < NSYMS; c++) {
            totals[c] = uint32(NSYMS);
        }
        if (warm) _prime(rows, totals);
        uint256 ctx = SYM_EOF;

        bytes memory out = new bytes(limit);
        uint256 outLen = 0;

        while (true) {
            uint32[] memory row = _row(rows, ctx);
            uint256 tot = totals[ctx];
            uint256 rng = high - low + 1;
            uint256 t = ((value - low + 1) * tot - 1) / rng;

            uint256 acc = 0;
            uint256 sym = NSYMS;
            uint256 lo = 0;
            uint256 hi = 0;
            for (uint256 s = 0; s < NSYMS; s++) {
                uint256 nxt = acc + row[s];
                if (nxt > t) {
                    sym = s;
                    lo = acc;
                    hi = nxt;
                    break;
                }
                acc = nxt;
            }
            if (sym == NSYMS) revert Corrupt();

            high = low + (rng * hi) / tot - 1;
            low = low + (rng * lo) / tot;
            while (true) {
                if (high < HALF) {} else if (low >= HALF) {
                    low -= HALF;
                    high -= HALF;
                    value -= HALF;
                } else if (low >= QUARTER && high < THREEQ) {
                    low -= QUARTER;
                    high -= QUARTER;
                    value -= QUARTER;
                } else {
                    break;
                }
                low = (low << 1) & MASK;
                high = ((high << 1) | 1) & MASK;
                value = ((value << 1) | _readBit(data, bitpos)) & MASK;
                bitpos++;
            }

            if (sym == SYM_EOF) {
                assembly ("memory-safe") {
                    mstore(out, outLen)
                }
                return out;
            }
            if (outLen >= limit) revert TooLong();
            out[outLen] = bytes1(uint8(sym));
            outLen++;

            _update(row, totals, ctx, sym);
            ctx = sym;
        }
        revert Corrupt();
    }

    /// @dev Mirrors Go NewModel: prime from ctx 0; decoding then starts at EOF.
    function _prime(uint32[][] memory rows, uint32[] memory totals) private pure {
        bytes memory p = PRIOR;
        uint256 ctx = 0;
        for (uint256 i = 0; i < p.length; i++) {
            uint256 sym = uint8(p[i]);
            _update(_row(rows, ctx), totals, ctx, sym);
            ctx = sym;
        }
    }

    function _update(uint32[] memory row, uint32[] memory totals, uint256 ctx, uint256 sym) private pure {
        row[sym] += INCREMENT;
        totals[ctx] += INCREMENT;
        if (totals[ctx] >= RESCALE_AT) {
            uint32 nt = 0;
            for (uint256 s = 0; s < NSYMS; s++) {
                uint32 halved = (row[s] + 1) / 2; // never zero
                row[s] = halved;
                nt += halved;
            }
            totals[ctx] = nt;
        }
    }

    function _row(uint32[][] memory rows, uint256 c) private pure returns (uint32[] memory r) {
        r = rows[c];
        if (r.length == 0) {
            r = new uint32[](NSYMS);
            for (uint256 i = 0; i < NSYMS; i++) {
                r[i] = 1;
            }
            rows[c] = r;
        }
    }

    /// @dev MSB-first; zero past the end, like the Go bitReader.
    function _readBit(bytes memory data, uint256 bitpos) private pure returns (uint256) {
        uint256 bytePos = bitpos >> 3;
        if (bytePos >= data.length) return 0;
        return (uint8(data[bytePos]) >> (7 - (bitpos & 7))) & 1;
    }
}
