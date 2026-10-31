#pragma once
#include <algorithm>
#include <cstdint>
#include <stdexcept>
#include <string>
#include <vector>

namespace probe {
using Bytes = std::vector<std::uint8_t>;

// require 拒绝无效数据并返回具体原因，避免把损坏输出交给浏览器。
inline void require(bool ok, const char* message) {
    if (!ok) throw std::runtime_error(message);
}

// startCode 返回指定偏移处的 Annex B 起始码长度，不把长度前缀流猜测为 Annex B。
inline std::size_t startCode(const Bytes& data, std::size_t p) {
    if (p + 3 <= data.size() && data[p] == 0 && data[p+1] == 0) {
        if (data[p+2] == 1) return 3;
        if (p + 4 <= data.size() && data[p+2] == 0 && data[p+3] == 1) return 4;
    }
    return 0;
}

// splitAnnexB 按三或四字节起始码提取 NAL，拒绝缺起始码、空 NAL 和异常头。
inline std::vector<Bytes> splitAnnexB(const Bytes& data) {
    std::vector<Bytes> nals;
    std::size_t p = 0;
    while (p < data.size() && !startCode(data, p) && data[p] == 0) ++p;
    require(p < data.size() && startCode(data, p), "expected Annex B start code");
    while (p < data.size()) {
        auto n = startCode(data, p);
        require(n != 0, "invalid Annex B boundary");
        auto begin = p + n;
        p = begin;
        while (p < data.size() && !startCode(data, p)) ++p;
        auto end = p;
        while (end > begin && data[end-1] == 0) --end;
        require(end > begin, "empty H264 NAL");
        require((data[begin] & 0x80) == 0 && (data[begin] & 31) != 0, "invalid H264 NAL header");
        nals.emplace_back(data.begin()+begin, data.begin()+end);
    }
    return nals;
}

struct Slice {
    unsigned first = 0, type = 0;
};

// sliceHeader 解读 first_mb 和 slice_type，识别一个 sample 内的多帧或违背无 B 帧约定的输出。
inline Slice sliceHeader(const Bytes& nal) {
    Bytes rbsp;
    unsigned zeros = 0;
    for (std::size_t i = 1; i < nal.size(); ++i) {
        auto b = nal[i];
        if (zeros >= 2 && b == 3) { zeros = 0; continue; }
        rbsp.push_back(b);
        zeros = b == 0 ? zeros + 1 : 0;
    }
    std::size_t bit = 0;
    // readBit 对 RBSP 做边界检查，截断切片头不能被当作有效帧。
    auto readBit = [&]() -> unsigned {
        require(bit < rbsp.size()*8, "truncated slice header");
        unsigned b = (rbsp[bit/8] >> (7-bit%8)) & 1;
        ++bit;
        return b;
    };
    // readUE 读取有限长度的无符号 Exp-Golomb 数，防止畸形数据溢出。
    auto readUE = [&]() -> unsigned {
        unsigned zerosCount = 0;
        while (!readBit()) { require(++zerosCount < 31, "slice integer overflow"); }
        unsigned value = 1;
        for (unsigned i = 0; i < zerosCount; ++i) value = (value << 1) | readBit();
        return value - 1;
    };
    Slice result;
    result.first = readUE();
    result.type = readUE();
    require(result.type <= 9 && result.type % 5 != 1, "B slice or invalid slice type");
    return result;
}

// appendNAL 写出统一四字节起始码，不改变 NAL 有效载荷。
inline void appendNAL(Bytes& out, const Bytes& nal) {
    out.insert(out.end(), {0,0,0,1});
    out.insert(out.end(), nal.begin(), nal.end());
}

struct AccessUnit { Bytes data; bool key = false; };

// videoPacket 将完整访问单元封装为既有17字节视频头，PTS从100ns转换为微秒，ready留给Go填入。
inline Bytes videoPacket(const AccessUnit& au, std::uint64_t pts100ns) {
    require(!au.data.empty()&&au.data.size()<=16u*1024u*1024u,"native AU size invalid");
    Bytes packet(17,0);
    packet[0]=au.key?1:0;
    auto us=pts100ns/10;
    for(unsigned i=0;i<8;++i) packet[1+i]=std::uint8_t(us>>(56-8*i));
    packet.insert(packet.end(),au.data.begin(),au.data.end());
    return packet;
}

// streamRecord 为一个完整视频包添加大端长度，允许管道任意拆分而不破坏帧边界。
inline Bytes streamRecord(const Bytes& packet) {
    require(packet.size()>17&&packet.size()<=(16u<<20)+17,"native packet size invalid");
    auto n=static_cast<std::uint32_t>(packet.size());
    Bytes record;
    for(unsigned i=0;i<4;++i) record.push_back(std::uint8_t(n>>(24-8*i)));
    record.insert(record.end(),packet.begin(),packet.end());
    return record;
}

struct H264 {
    Bytes sps, pps;
    // parameters 缓存系统输出媒体类型中的 Annex B SPS/PPS，用于恢复关键帧。
    void parameters(const Bytes& data) {
        for (const auto& n : splitAnnexB(data)) {
            if ((n[0]&31) == 7) sps = n;
            if ((n[0]&31) == 8) pps = n;
        }
    }
    // normalize 验证一个完整 sample 的帧边界，插入 AUD，并在 IDR 前补齐参数集。
    AccessUnit normalize(const Bytes& data) {
        auto nals = splitAnnexB(data);
        bool hasVCL = false, key = false, nonKey = false;
        unsigned starts = 0;
        for (const auto& n : nals) {
            unsigned type = n[0]&31;
            if (type == 7) sps = n;
            if (type == 8) pps = n;
            if (type >= 1 && type <= 5) {
                require(type == 1 || type == 5, "partitioned H264 slices unsupported");
                auto slice = sliceHeader(n);
                if (!hasVCL) require(slice.first == 0, "sample starts mid-frame");
                if (slice.first == 0) ++starts;
                hasVCL = true;
                key |= type == 5;
                nonKey |= type == 1;
            }
        }
        if (!hasVCL) return {};
        require(starts == 1 && !(key && nonKey), "sample contains multiple access units");
        require(sps.size() >= 4 && !pps.empty(), "H264 output missing SPS/PPS");
        AccessUnit out;
        out.key = key;
        appendNAL(out.data, {9,0xf0});
        if (key) { appendNAL(out.data, sps); appendNAL(out.data, pps); }
        for (const auto& n : nals) {
            unsigned type = n[0]&31;
            if (type != 9 && type != 7 && type != 8) appendNAL(out.data, n);
        }
        return out;
    }
};

// base64 将完整协议包编码进离线 HTML，不引入目标机脚本语言或编码软件。
inline std::string base64(const Bytes& data) {
    const char* alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    std::string out;
    for (std::size_t i=0; i<data.size(); i+=3) {
        unsigned n = unsigned(data[i]) << 16;
        if (i+1<data.size()) n |= unsigned(data[i+1]) << 8;
        if (i+2<data.size()) n |= data[i+2];
        out += alphabet[(n>>18)&63]; out += alphabet[(n>>12)&63];
        out += i+1<data.size() ? alphabet[(n>>6)&63] : '=';
        out += i+2<data.size() ? alphabet[n&63] : '=';
    }
    return out;
}
}
