FROM mcr.microsoft.com/oss/go/microsoft/golang:1.24.5-bookworm@sha256:4bf921879edc69ca12c81de48583f4de79acab97e56d2f9371c31da6ac6b44d9

RUN apt-get update && export DEBIAN_FRONTEND=noninteractive && \
    apt-get -y install dosfstools mtools gdisk fdisk p7zip genisoimage squashfs-tools && \
    echo 'mtools_skip_check=1' | tee -a /etc/mtools.conf
