class Joshbot < Formula
  # The canonical copy of this formula lives in the tap:
  #   https://github.com/bigknoxy/homebrew-joshbot
  # (`brew install bigknoxy/joshbot/joshbot`)
  # Keep this file in sync when cutting a release: bump `url`, `sha256`, and
  # the version ldflag below. Homebrew prints the sha it expected on a
  # mismatch, or compute one directly:
  #   curl -fsSL <url> | shasum -a 256
  desc "Nanobot-class personal AI agent as a single static Go binary"
  homepage "https://github.com/bigknoxy/joshbot"
  url "https://github.com/bigknoxy/joshbot/archive/refs/tags/v1.72.0.tar.gz"
  sha256 "0614c96582fceb076468ded39af61bf34665ef57b41782ca722b254d5b895ce8"
  license "MIT"
  head "https://github.com/bigknoxy/joshbot.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X main.Version=#{version}"), "./cmd/joshbot"
  end

  def caveats
    <<~EOS
      See it work before setting anything up:

        joshbot demo

      Then get a real model connected:

        joshbot onboard
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/joshbot version")
  end
end
