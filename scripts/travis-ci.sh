#!/bin/bash -eux

# Which make target to run.
WHAT="$1"
# Because we need to compile some Go code without modules,
# the source must be placed in a specific directory as expected by Go.
# The path is relative to GOPATH.
# If you change this, remember to update MACOS_BUILD in
# travis_upload_nightlies.sh.
GO_SRC_DIR=src/github.com/digitalbitbox/bitbox-wallet-app

# The following is executed only on linux machines.
if [ "$TRAVIS_OS_NAME" == "linux" ]; then
    # Which docker image to use to run the CI. Defaults to Docker Hub.
    # Overwrite with CI_IMAGE=docker/image/path environment variable.
    # Keep this in sync with .github/workflows/ci.yml.
    : "${CI_IMAGE:=shiftcrypto/bitbox-wallet-app:6}"
    # Time image pull to compare in the future.
    time docker pull "$CI_IMAGE"

    docker run --privileged \
           -v ${TRAVIS_BUILD_DIR}:/opt/go/${GO_SRC_DIR}/ \
           -i "${CI_IMAGE}" \
           bash -c "make -C \$GOPATH/${GO_SRC_DIR} ${WHAT}"
fi

# The following is executed only on macOS machines.
if [ "$TRAVIS_OS_NAME" == "osx" ]; then
    export HOMEBREW_NO_AUTO_UPDATE=1
    brew outdated go || brew upgrade go
    go version
    # Build Qt from source.
    # https://doc-snapshots.qt.io/qt5-5.12/macos-building.html
    curl -sSL -o qt-src-5.11.3.tar.xz https://download.qt.io/new_archive/qt/5.11/5.11.3/single/qt-everywhere-src-5.11.3.tar.xz
    tar -xf qt-src-5.11.3.tar.xz
    pushd qt-everywhere-src-5.11.3
    # https://doc-snapshots.qt.io/qt5-5.12/configure-options.html
    #./configure -list-features
    ./configure -prefix $PWD/../qt5.11.3 \
        -release \
        -opensource \
        -confirm-license \
        -no-compile-examples \
        -no-cups \
        -no-gtk \
        -no-feature-futimens \
        -skip qt3d \
        -skip qtactiveqt \
        -skip qtandroidextras \
        -skip qtcanvas3d \
        -skip qtcharts \
        -skip qtconnectivity \
        -skip qtdatavis3d \
        -skip qtdoc \
        -skip qtgamepad \
        -skip qtgraphicaleffects \
        -skip qtlocation \
        -skip qtpurchasing \
        -skip qtspeech \
        -skip qtvirtualkeyboard \
        -skip qtwayland \
        -skip qtwinextras \
        -skip qtx11extras
    make -s
    make -j1 install
    popd
    du -sh qt5.11.3
    ls -lh qt5.11.3
    export PATH="$PWD/qt5.11.3/bin:$PATH"
    export LDFLAGS="-L$PWD/qt5.11.3/lib"
    export CPPFLAGS="-I$PWD/qt5.11.3/include"
    # Install yarn only if it isn't already.
    # GitHub runners already have node and yarn installed which makes homebrew
    # fail due to conflicting files.
    type yarn > /dev/null || brew install yarn
    brew install nvm
    source /usr/local/opt/nvm/nvm.sh
    nvm install 10.16.3 # install this node version
    export GOPATH=~/go/
    export PATH=$PATH:~/go/bin
    mkdir -p $GOPATH/$(dirname $GO_SRC_DIR)
    # GitHub checkout action (git clone) seem to require current work dir
    # to be the root of the repo during its clean up phase. So, we push it
    # here and pop in the end.
    pushd ../ && cp -a bitbox-wallet-app $GOPATH/$(dirname $GO_SRC_DIR)
    cd $GOPATH/$GO_SRC_DIR
    make "$WHAT"
    popd
fi
