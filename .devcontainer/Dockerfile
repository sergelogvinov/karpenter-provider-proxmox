FROM mcr.microsoft.com/oss/go/microsoft/golang:1.27.1

RUN apt-get update && export DEBIAN_FRONTEND=noninteractive && \
    apt-get -y install build-essential procps curl file git && \
    apt-get -y install dosfstools mtools gdisk fdisk p7zip genisoimage squashfs-tools && \
    echo 'mtools_skip_check=1' | tee -a /etc/mtools.conf

RUN git clone https://github.com/Homebrew/brew /usr/homebrew && \
    mkdir -p /usr/homebrew/bin /usr/local/var/homebrew /usr/local/Cellar /usr/local/opt /usr/local/etc/bash_completion.d && \
    chown -R 1000:1000 /usr/homebrew /usr/local/var/homebrew /usr/local/Cellar /usr/local/opt && \
    chown -R 1000:1000 /usr/local/bin /usr/local/etc /usr/local/include /usr/local/lib /usr/local/sbin /usr/local/share && \
    mkdir -p /usr/local/share/locale /usr/local/share/man /usr/local/share/zsh /usr/local/share/zsh /usr/local/share/zsh/site-functions && \
    chown -R 1000:1000 /usr/local/share/locale /usr/local/share/man /usr/local/share/zsh /usr/local/share/zsh/site-functions && \
    ln -s /usr/homebrew/bin/brew /usr/local/bin/brew
