namespace GameAgent.Stardew.Tasks;

public interface ITaskNpcDriver
{
    WorldPosition ReadPosition(string npcEntityId);
    DriverResult StartTravel(OperationKey operation, Landmark landmark);
    DriverResult Poll(OperationKey operation);
    bool Transfer(OperationKey from, OperationKey to);
    bool Owns(OperationKey operation);
    void Release(OperationKey operation, string reason);
}
