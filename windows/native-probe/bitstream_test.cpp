#include "bitstream.hpp"
#include <iostream>
#include <fstream>
#include <iterator>

// main 验证边界拒绝和Base64；可选参数为已有40帧Annex B文件，不把测试描述为真实Windows编码。
int main(int argc,char** argv) {
    using namespace probe;
    try {
        H264 h;
        h.parameters({0,0,0,1,0x67,0x42,0xc0,0x28,0,0,1,0x68,0xce,0x3c,0x80});
        auto key=h.normalize({0,0,1,0x65,0xb8,0x80});
        require(key.key && splitAnnexB(key.data).size()==4,"IDR normalization");
        auto delta=h.normalize({0,0,0,1,9,0xf0,0,0,1,0x41,0xe0});
        require(!delta.key && splitAnnexB(delta.data).size()==2,"delta normalization");
        require(base64({})=="" && base64({1})=="AQ==" && base64({1,2})=="AQI=" && base64({1,2,3})=="AQID","base64");
        auto packet=videoPacket(AccessUnit{{0,0,0,1,9,0xf0},true},500000);
        require(packet.size()==23&&packet[0]==1&&packet[7]==0xc3&&packet[8]==0x50,"17-byte protocol / 100ns to microseconds");
        for(unsigned i=9;i<17;++i) require(packet[i]==0,"ready must be filled by Go");
        auto record=streamRecord(packet);
        require(record.size()==27&&record[0]==0&&record[1]==0&&record[2]==0&&record[3]==23,"length-prefixed native IPC");
        for (const Bytes& bad : std::vector<Bytes>{{1,2,3},{0,0,1},{0,0,1,0x81},{0,0,1,0x41,0xa0},{0,0,1,0x41,0xe0,0,0,1,0x41,0xe0}}) {
            bool rejected=false;
            try { h.normalize(bad); } catch(const std::runtime_error&) {rejected=true;}
            require(rejected,"invalid or multi-frame sample was accepted");
        }
        if(argc==2) {
            std::ifstream file(argv[1],std::ios::binary);
            require(file.good(),"cannot open real H264 fixture");
            Bytes bytes((std::istreambuf_iterator<char>(file)),std::istreambuf_iterator<char>());
            H264 real;Bytes au;unsigned frames=0,keys=0;
            for(const auto& nal:splitAnnexB(bytes)) {
                if((nal[0]&31)==9&&!au.empty()) {
                    auto frame=real.normalize(au);require(!frame.data.empty(),"fixture AU has no image");
                    ++frames;keys+=frame.key;au.clear();
                }
                appendNAL(au,nal);
            }
            auto last=real.normalize(au);require(!last.data.empty(),"fixture final AU missing");
            ++frames;keys+=last.key;
            require(frames==40&&keys==2,"real fixture frame/IDR counts");
            std::cout<<"PASS: real H264 fixture, 40 AU / 2 IDR (not Windows MF output)\n";
        }
        std::cout << "PASS: Annex B, AUD, SPS/PPS, IDR, B/multiple-frame rejection, Base64; no Windows GUI test\n";
        return 0;
    } catch(const std::exception& e) {std::cerr<<e.what()<<'\n';return 1;}
}
