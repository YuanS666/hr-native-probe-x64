#include "page.hpp"
#include <fstream>
#include <iostream>
#include <iterator>
#include <stdexcept>
#include <string>

// main 将嵌入数组与参数指定的源文件逐字节比较，验证UTF-8、尾部及NUL终止均未丢失。
int main(int argc,char** argv) {
    try {
        if(argc!=3) throw std::runtime_error("expected HTML and JavaScript source paths");
        const char* data[2]={probePage,probeScript};
        const std::size_t sizes[2]={sizeof(probePageData),sizeof(probeScriptData)};
        for(int i=0;i<2;++i) {
            std::ifstream source(argv[i+1],std::ios::binary);
            if(!source) throw std::runtime_error("cannot read embedded-page source");
            std::string original((std::istreambuf_iterator<char>(source)),std::istreambuf_iterator<char>());
            if(sizes[i]!=original.size()+1 || data[i][sizes[i]-1]!='\0' ||
               std::string(data[i],sizes[i]-1)!=original || std::string(data[i])!=original)
                throw std::runtime_error("embedded page/script differs from source bytes");
        }
        std::cout<<"PASS: embedded HTML and JavaScript match original bytes, including UTF-8; no literal truncation\n";
        return 0;
    } catch(const std::exception& e) {std::cerr<<e.what()<<'\n';return 1;}
}
