Summary:            Hyperstart is the initrd for hyper VM
Name:               hyperstart
Version:            0.5
Release:            1%{?dist}
License:            Apache License, Version 2.0
Group:              System Environment/Base
# The source for this package was pulled from upstream's git repo. Use the
# following commands to generate the tarball:
#  git archive --format=tar.gz master > hyperstart-%{version}.tar.gz
Source0:            %{name}-%{version}.tar.gz
#  git archive --format=tar.gz master > qboot.tar.gz
Source1:            qboot.tar.gz
URL:                https://hyper.sh/
ExclusiveArch:      x86_64

%description
Hyperstart is the initrd for hyper VM, hyperstart 
includes the kernel and initrd, qboot bios and cbfs rom
image.

%prep
mkdir -p %{_builddir}/src/github.com/hyperhq/hyperstart
tar -C %{_builddir}/src/github.com/hyperhq/hyperstart -xvf %SOURCE0
mkdir -p %{_builddir}/src/qboot
tar -C %{_builddir}/src/qboot -xvf %SOURCE1

%build
cd %{_builddir}/src/github.com/hyperhq/hyperstart
./autogen.sh
./configure
make %{?_smp_mflags}
make cbfs
cd %{_builddir}/src/qboot
make

%install
mkdir -p %{buildroot}%{_sharedstatedir}/hyper
cp %{_builddir}/src/github.com/hyperhq/hyperstart/build/kernel %{buildroot}%{_sharedstatedir}/hyper/
cp %{_builddir}/src/github.com/hyperhq/hyperstart/build/hyper-initrd.img %{buildroot}%{_sharedstatedir}/hyper/
cp %{_builddir}/src/github.com/hyperhq/hyperstart/build/cbfs.rom %{buildroot}%{_sharedstatedir}/hyper/cbfs-qboot.rom
cp %{_builddir}/src/qboot/bios.bin %{buildroot}%{_sharedstatedir}/hyper/bios-qboot.bin

%clean
rm -rf %{buildroot}

%files
%{_sharedstatedir}/*

%changelog
* Sat Jan 30 2016 Xu Wang <xu@hyper.sh> - 0.5-1
- update source to 0.5
* Fri Jan 29 2016 Xu Wang <xu@hyper.sh> - 0.4-2
- Fix firmware path
* Sat Nov 21 2015 Xu Wang <xu@hyper.sh> - 0.4-1
- Initial rpm packaging for hyperstart
