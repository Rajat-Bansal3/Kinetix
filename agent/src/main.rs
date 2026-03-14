use std::net::IpAddr;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    println!("Hello, world!");
    let addr: IpAddr = "127.0.0.1:5005".parse()?;
    println!("Engine running on {}", addr);
    Ok(())
}
